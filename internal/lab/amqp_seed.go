package lab

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"
)

const (
	amqpFrameMethod  = 1
	amqpFrameHeader  = 2
	amqpFrameBody    = 3
	amqpFrameEnd     = 0xCE
	amqpClassConn    = 10
	amqpClassChannel = 20
	amqpClassQueue   = 50
	amqpClassBasic   = 60
)

// SeedAMQP dials AMQP 0-9-1 at host:port, declares queue, and publishes each
// body. Credentials come from RABBITMQ_* env (honeytokens on the seed pod)
// or guest/guest. No external AMQP module — framing is stdlib only.
func SeedAMQP(host string, port int, queue string, bodies []string) error {
	if host == "" || port <= 0 {
		return fmt.Errorf("lab-seed-amqp needs --host and --port")
	}
	if queue == "" {
		queue = seedQueue
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	user := firstNonEmpty(os.Getenv("RABBITMQ_DEFAULT_USER"), os.Getenv("RABBITMQ_USER"), os.Getenv("RABBITMQ_DEFAULT_USERNAME"), "guest")
	pass := firstNonEmpty(os.Getenv("RABBITMQ_DEFAULT_PASS"), os.Getenv("RABBITMQ_PASSWORD"), os.Getenv("RABBITMQ_DEFAULT_PASSWORD"), "guest")
	if err := amqpHandshake(conn, user, pass); err != nil {
		return err
	}
	if err := amqpMethod(conn, 1, amqpClassQueue, 10, amqpQueueDeclareArgs(queue)); err != nil {
		return fmt.Errorf("queue.declare: %w", err)
	}
	if _, _, _, err := amqpExpect(conn, 1, amqpClassQueue, 11); err != nil {
		return fmt.Errorf("queue.declare-ok: %w", err)
	}
	for _, body := range bodies {
		if err := amqpPublish(conn, 1, queue, []byte(body)); err != nil {
			return err
		}
	}
	_ = amqpMethod(conn, 0, amqpClassConn, 50, amqpConnectionCloseArgs())
	_, _, _, _ = amqpExpect(conn, 0, amqpClassConn, 51)
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func amqpHandshake(conn net.Conn, user, pass string) error {
	if _, err := conn.Write([]byte{'A', 'M', 'Q', 'P', 0, 0, 9, 1}); err != nil {
		return err
	}
	if _, _, _, err := amqpExpect(conn, 0, amqpClassConn, 10); err != nil {
		return fmt.Errorf("connection.start: %w", err)
	}
	response := "\x00" + user + "\x00" + pass
	if err := amqpMethod(conn, 0, amqpClassConn, 11, amqpStartOKArgs(response)); err != nil {
		return fmt.Errorf("connection.start-ok: %w", err)
	}
	if _, _, args, err := amqpExpect(conn, 0, amqpClassConn, 30); err != nil {
		return fmt.Errorf("connection.tune: %w", err)
	} else {
		channelMax, frameMax, heartbeat := amqpParseTune(args)
		if err := amqpMethod(conn, 0, amqpClassConn, 31, amqpTuneOKArgs(channelMax, frameMax, heartbeat)); err != nil {
			return fmt.Errorf("connection.tune-ok: %w", err)
		}
	}
	if err := amqpMethod(conn, 0, amqpClassConn, 40, amqpOpenArgs("/")); err != nil {
		return fmt.Errorf("connection.open: %w", err)
	}
	if _, _, _, err := amqpExpect(conn, 0, amqpClassConn, 41); err != nil {
		return fmt.Errorf("connection.open-ok: %w", err)
	}
	if err := amqpMethod(conn, 1, amqpClassChannel, 10, []byte{0, 0}); err != nil {
		return fmt.Errorf("channel.open: %w", err)
	}
	if _, _, _, err := amqpExpect(conn, 1, amqpClassChannel, 11); err != nil {
		return fmt.Errorf("channel.open-ok: %w", err)
	}
	return nil
}

func amqpPublish(conn net.Conn, channel uint16, routingKey string, body []byte) error {
	if err := amqpMethod(conn, channel, amqpClassBasic, 40, amqpPublishArgs("", routingKey)); err != nil {
		return fmt.Errorf("basic.publish: %w", err)
	}
	header := make([]byte, 14)
	binary.BigEndian.PutUint16(header[0:], amqpClassBasic)
	binary.BigEndian.PutUint16(header[2:], 0) // weight
	binary.BigEndian.PutUint64(header[4:], uint64(len(body)))
	binary.BigEndian.PutUint16(header[12:], 0) // property flags
	if err := amqpWriteFrame(conn, amqpFrameHeader, channel, header); err != nil {
		return err
	}
	return amqpWriteFrame(conn, amqpFrameBody, channel, body)
}

func amqpMethod(conn net.Conn, channel uint16, class, method uint16, args []byte) error {
	payload := make([]byte, 4+len(args))
	binary.BigEndian.PutUint16(payload[0:], class)
	binary.BigEndian.PutUint16(payload[2:], method)
	copy(payload[4:], args)
	return amqpWriteFrame(conn, amqpFrameMethod, channel, payload)
}

func amqpWriteFrame(conn net.Conn, kind byte, channel uint16, payload []byte) error {
	frame := make([]byte, 8+len(payload))
	frame[0] = kind
	binary.BigEndian.PutUint16(frame[1:], channel)
	binary.BigEndian.PutUint32(frame[3:], uint32(len(payload)))
	copy(frame[7:], payload)
	frame[7+len(payload)] = amqpFrameEnd
	_, err := conn.Write(frame)
	return err
}

func amqpExpect(conn net.Conn, channel uint16, class, method uint16) (uint16, uint16, []byte, error) {
	for {
		kind, ch, payload, err := amqpReadFrame(conn)
		if err != nil {
			return 0, 0, nil, err
		}
		if kind != amqpFrameMethod || len(payload) < 4 {
			continue
		}
		gotClass := binary.BigEndian.Uint16(payload[0:])
		gotMethod := binary.BigEndian.Uint16(payload[2:])
		if ch == channel && gotClass == class && gotMethod == method {
			return gotClass, gotMethod, payload[4:], nil
		}
		if gotClass == amqpClassConn && gotMethod == 50 { // connection.close
			return 0, 0, nil, fmt.Errorf("broker closed connection")
		}
		if gotClass == amqpClassChannel && gotMethod == 40 { // channel.close
			return 0, 0, nil, fmt.Errorf("broker closed channel")
		}
	}
}

func amqpReadFrame(conn net.Conn) (byte, uint16, []byte, error) {
	var hdr [7]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return 0, 0, nil, err
	}
	size := binary.BigEndian.Uint32(hdr[3:])
	if size > 1<<20 {
		return 0, 0, nil, fmt.Errorf("amqp frame too large: %d", size)
	}
	payload := make([]byte, size+1)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return 0, 0, nil, err
	}
	if payload[size] != amqpFrameEnd {
		return 0, 0, nil, fmt.Errorf("bad amqp frame end")
	}
	return hdr[0], binary.BigEndian.Uint16(hdr[1:]), payload[:size], nil
}

func amqpStartOKArgs(response string) []byte {
	var b []byte
	b = append(b, amqpTable(nil)...)
	b = append(b, amqpShortStr("PLAIN")...)
	b = append(b, amqpLongStr(response)...)
	b = append(b, amqpShortStr("en_US")...)
	return b
}

func amqpParseTune(args []byte) (channelMax uint16, frameMax uint32, heartbeat uint16) {
	channelMax, frameMax, heartbeat = 0, 131072, 60
	if len(args) >= 8 {
		channelMax = binary.BigEndian.Uint16(args[0:])
		frameMax = binary.BigEndian.Uint32(args[2:])
		heartbeat = binary.BigEndian.Uint16(args[6:])
	}
	if channelMax == 0 {
		channelMax = 2047
	}
	if frameMax < 4096 {
		frameMax = 131072
	}
	return channelMax, frameMax, heartbeat
}

func amqpTuneOKArgs(channelMax uint16, frameMax uint32, heartbeat uint16) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint16(b[0:], channelMax)
	binary.BigEndian.PutUint32(b[2:], frameMax)
	binary.BigEndian.PutUint16(b[6:], heartbeat)
	return b
}

func amqpOpenArgs(vhost string) []byte {
	var b []byte
	b = append(b, amqpShortStr(vhost)...)
	b = append(b, 0)       // capabilities empty
	b = append(b, 0)       // insist
	return b
}

func amqpConnectionCloseArgs() []byte {
	var out []byte
	out = append(out, 0, 200) // reply-code 200
	out = append(out, amqpShortStr("OK")...)
	out = append(out, 0, 0, 0, 0) // class-id, method-id
	return out
}

func amqpQueueDeclareArgs(name string) []byte {
	var b []byte
	b = append(b, 0, 0) // reserved-1
	b = append(b, amqpShortStr(name)...)
	b = append(b, 1<<1)              // durable (bit 1); passive/exclusive/auto-delete/no-wait off
	b = append(b, amqpTable(nil)...) // arguments
	return b
}

func amqpPublishArgs(exchange, routingKey string) []byte {
	var b []byte
	b = append(b, 0, 0) // reserved-1
	b = append(b, amqpShortStr(exchange)...)
	b = append(b, amqpShortStr(routingKey)...)
	b = append(b, 0) // mandatory=0 immediate=0
	return b
}

func amqpShortStr(s string) []byte {
	if len(s) > 255 {
		s = s[:255]
	}
	b := make([]byte, 1+len(s))
	b[0] = byte(len(s))
	copy(b[1:], s)
	return b
}

func amqpLongStr(s string) []byte {
	b := make([]byte, 4+len(s))
	binary.BigEndian.PutUint32(b, uint32(len(s)))
	copy(b[4:], s)
	return b
}

func amqpTable(entries map[string]string) []byte {
	var inner []byte
	for k, v := range entries {
		inner = append(inner, amqpShortStr(k)...)
		inner = append(inner, 'S')
		inner = append(inner, amqpLongStr(v)...)
	}
	b := make([]byte, 4+len(inner))
	binary.BigEndian.PutUint32(b, uint32(len(inner)))
	copy(b[4:], inner)
	return b
}
