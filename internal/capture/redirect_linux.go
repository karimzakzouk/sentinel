//go:build linux

package capture

import (
	"encoding/binary"
	"fmt"
	"os"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// RedirectTable is the nftables NAT table that steers TCP/443 to the local MITM.
const RedirectTable = "sentinel_capture_mitm"

// ApplyRedirect installs host NAT REDIRECT rules for TCP dport 443 into listenPort.
// Traffic from this process's UID is left alone so upstream dials do not loop.
func ApplyRedirect(listenPort uint16) error {
	if listenPort == 0 {
		return fmt.Errorf("redirect needs a listen port")
	}
	c, err := nftables.New()
	if err != nil {
		return fmt.Errorf("open netfilter: %w", err)
	}
	_ = ClearRedirect()
	uid := uint32(os.Geteuid())
	table := c.AddTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: RedirectTable})
	for _, ch := range []*nftables.Chain{
		{Name: "prerouting", Table: table, Type: nftables.ChainTypeNAT,
			Hooknum: nftables.ChainHookPrerouting, Priority: nftables.ChainPriorityNATDest},
		{Name: "output", Table: table, Type: nftables.ChainTypeNAT,
			Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityNATDest},
	} {
		chain := c.AddChain(ch)
		// Skip packets owned by the MITM process (its own upstream dials).
		c.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeySKUID, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(uid)},
			&expr.Verdict{Kind: expr.VerdictReturn},
		}})
		c.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: redirectExprs(listenPort)})
	}
	if err := c.Flush(); err != nil {
		return fmt.Errorf("install capture redirect: %w", err)
	}
	return nil
}

// ClearRedirect removes the capture MITM NAT table when present.
func ClearRedirect() error {
	c, err := nftables.New()
	if err != nil {
		return err
	}
	c.DelTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: RedirectTable})
	if err := c.Flush(); err != nil {
		// Missing table is fine on first install.
		return nil
	}
	return nil
}

func redirectExprs(port uint16) []expr.Any {
	portBE := make([]byte, 2)
	binary.BigEndian.PutUint16(portBE, 443)
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: portBE},
		&expr.Immediate{Register: 1, Data: binaryutil.BigEndian.PutUint16(port)},
		&expr.Redir{RegisterProtoMin: 1},
	}
}
