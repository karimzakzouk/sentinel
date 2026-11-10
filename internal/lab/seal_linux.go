//go:build linux

package lab

import (
	"fmt"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// SealTable is the nftables table the firewall installs.
const SealTable = "sentinel_lab_seal"

// ApplySeal seals the network namespace this process runs in: new
// connections may go only to allow and over loopback; replies to
// connections made to the pod still go out, so its probes and clients are
// answered. Everything else is refused, at once where the kernel can say
// so and by dropping otherwise. It returns how many rules it installed.
func ApplySeal(allow []Allow) (int, error) {
	c, err := nftables.New()
	if err != nil {
		return 0, fmt.Errorf("open netfilter: %w", err)
	}
	n, err := applySeal(c, allow, true)
	if err == nil {
		return n, nil
	}
	// A batch is atomic, so a kernel without reject in the inet family
	// left nothing behind; the drop policy alone still seals.
	n, err2 := applySeal(c, allow, false)
	if err2 != nil {
		return 0, fmt.Errorf("install the seal: %w (and without refusals: %w)", err, err2)
	}
	return n, nil
}

func applySeal(c *nftables.Conn, allow []Allow, refuse bool) (int, error) {
	table := c.AddTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: SealTable})
	policy := nftables.ChainPolicyDrop
	out := c.AddChain(&nftables.Chain{Name: "output", Table: table, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityFilter, Policy: &policy})
	rules := sealRules(allow, refuse)
	for _, exprs := range rules {
		c.AddRule(&nftables.Rule{Table: table, Chain: out, Exprs: exprs})
	}
	if err := c.Flush(); err != nil {
		return 0, err
	}
	got, err := c.GetRules(table, out)
	if err != nil {
		return 0, fmt.Errorf("read the seal back: %w", err)
	}
	if len(got) != len(rules) {
		return 0, fmt.Errorf("the seal has %d rules, not %d", len(got), len(rules))
	}
	return len(got), nil
}

// sealRules are the output chain's rules, in order, before its drop policy.
func sealRules(allow []Allow, refuse bool) [][]expr.Any {
	accept := &expr.Verdict{Kind: expr.VerdictAccept}
	rules := [][]expr.Any{
		{&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ifname("lo")}, accept},
		{
			&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
			&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4,
				Mask: binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED),
				Xor:  binaryutil.NativeEndian.PutUint32(0)},
			&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0, 0, 0, 0}},
			accept,
		},
	}
	for _, a := range allow {
		rules = append(rules, append(destination(a), accept))
	}
	if refuse {
		rules = append(rules,
			[]expr.Any{&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
				&expr.Reject{Type: unix.NFT_REJECT_TCP_RST}},
			[]expr.Any{&expr.Reject{Type: unix.NFT_REJECT_ICMPX_UNREACH, Code: unix.NFT_REJECT_ICMPX_ADMIN_PROHIBITED}})
	}
	return rules
}

// destination matches packets to a's address, protocol and port.
func destination(a Allow) []expr.Any {
	ip := a.Addr.Addr()
	var exprs []expr.Any
	if ip.Is4() {
		b := ip.As4()
		exprs = append(exprs, &expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.NFPROTO_IPV4}},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: b[:]})
	} else {
		b := ip.As16()
		exprs = append(exprs, &expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.NFPROTO_IPV6}},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 24, Len: 16}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: b[:]})
	}
	proto := byte(unix.IPPROTO_TCP)
	if a.Proto == "udp" {
		proto = unix.IPPROTO_UDP
	}
	return append(exprs, &expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(a.Addr.Port())})
}

// ifname is an interface name as the kernel compares it: IFNAMSIZ bytes.
func ifname(n string) []byte {
	b := make([]byte, unix.IFNAMSIZ)
	copy(b, n)
	return b
}
