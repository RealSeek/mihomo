package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/miekg/dns"
)

type receipt struct {
	Transport   string `json:"transport"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

type probeResult struct {
	Command string  `json:"command"`
	Local   string  `json:"local"`
	Remote  string  `json:"remote"`
	EchoOK  bool    `json:"echo_ok"`
	Server  receipt `json:"server"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: socket-probe serve|probe|dns [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "probe", "client":
		err = probe(os.Args[2:])
	case "dns":
		err = queryDNS(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	tcpAddr := flags.String("tcp", ":41200", "TCP listen address")
	udpAddr := flags.String("udp", ":41201", "UDP listen address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	tcp, err := net.Listen("tcp", *tcpAddr)
	if err != nil {
		return err
	}
	defer tcp.Close()
	udp, err := net.ListenPacket("udp", *udpAddr)
	if err != nil {
		return err
	}
	defer udp.Close()
	printJSON(map[string]string{"command": "serve", "tcp": tcp.Addr().String(), "udp": udp.LocalAddr().String()})
	errCh := make(chan error, 2)
	go func() {
		for {
			conn, err := tcp.Accept()
			if err != nil {
				errCh <- err
				return
			}
			go func() {
				defer conn.Close()
				if err := echoTCP(conn); err != nil {
					fmt.Fprintln(os.Stderr, "TCP", conn.RemoteAddr(), err)
				}
			}()
		}
	}()
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := udp.ReadFrom(buf)
			if err != nil {
				errCh <- err
				return
			}
			meta := newReceipt("udp", buf[:n], addr.String(), udp.LocalAddr().String())
			data, err := json.Marshal(meta)
			if err == nil {
				_, err = udp.WriteTo(data, addr)
			}
			if err == nil {
				_, err = udp.WriteTo(buf[:n], addr)
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, "UDP", addr, err)
			} else {
				printJSON(meta)
			}
		}
	}()
	return <-errCh
}

func echoTCP(conn net.Conn) error {
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return err
	}
	payload, err := readFrame(conn)
	if err != nil {
		return err
	}
	meta := newReceipt("tcp", payload, conn.RemoteAddr().String(), conn.LocalAddr().String())
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if err := writeFrame(conn, data); err != nil {
		return err
	}
	if err := writeFrame(conn, payload); err != nil {
		return err
	}
	printJSON(meta)
	return nil
}

func probe(args []string) error {
	flags := flag.NewFlagSet("probe", flag.ContinueOnError)
	host := flags.String("host", "", "overlay IP or hostname")
	tcpPort := flags.Int("tcp-port", 41200, "TCP port")
	udpPort := flags.Int("udp-port", 41201, "UDP port")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *host == "" {
		return errors.New("-host is required")
	}
	var errs []error
	for _, transport := range []string{"tcp", "udp"} {
		port, size := *tcpPort, 2*1024*1024
		if transport == "udp" {
			port, size = *udpPort, 1280
		}
		if err := probeSocket(transport, net.JoinHostPort(*host, strconv.Itoa(port)), size); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", transport, err))
		}
	}
	return errors.Join(errs...)
}

func probeSocket(transport, address string, size int) error {
	conn, err := net.DialTimeout(transport, address, 15*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return err
	}
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte((i*31 + i/251 + 17) % 256)
	}
	var meta receipt
	var echoed []byte
	if transport == "tcp" {
		if err := writeFrame(conn, payload); err != nil {
			return err
		}
		data, err := readFrame(conn)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			return err
		}
		echoed, err = readFrame(conn)
		if err != nil {
			return err
		}
	} else {
		if _, err := conn.Write(payload); err != nil {
			return err
		}
		buf := make([]byte, 65535)
		for range 2 {
			n, err := conn.Read(buf)
			if err != nil {
				return err
			}
			if bytes.Equal(buf[:n], payload) {
				echoed = bytes.Clone(buf[:n])
			} else if err := json.Unmarshal(buf[:n], &meta); err != nil {
				return err
			}
		}
	}
	ok := bytes.Equal(echoed, payload) && meta.Transport == transport && meta.Bytes == size && meta.SHA256 == payloadHash(payload)
	printJSON(probeResult{Command: "probe", Local: conn.LocalAddr().String(), Remote: conn.RemoteAddr().String(), EchoOK: ok, Server: meta})
	if !ok {
		return errors.New("echo or server SHA256 mismatch")
	}
	return nil
}

func queryDNS(args []string) error {
	flags := flag.NewFlagSet("dns", flag.ContinueOnError)
	server := flags.String("server", "127.0.0.1:15353", "DNS server address")
	name := flags.String("name", "desktop.et.net", "name or PTR IP address")
	typeName := flags.String("type", "A", "A, AAAA or PTR")
	network := flags.String("network", "udp", "DNS transport: udp or tcp")
	if err := flags.Parse(args); err != nil {
		return err
	}
	var queryType uint16
	switch strings.ToUpper(*typeName) {
	case "A":
		queryType = dns.TypeA
	case "AAAA":
		queryType = dns.TypeAAAA
	case "PTR":
		queryType = dns.TypePTR
		if net.ParseIP(*name) != nil {
			reverse, err := dns.ReverseAddr(*name)
			if err != nil {
				return err
			}
			*name = reverse
		}
	default:
		return fmt.Errorf("unsupported DNS type %q", *typeName)
	}
	query := new(dns.Msg)
	query.SetQuestion(dns.Fqdn(*name), queryType)
	client := dns.Client{Net: *network, Timeout: 15 * time.Second}
	answer, elapsed, err := client.Exchange(query, *server)
	if err != nil {
		return err
	}
	fmt.Printf("server=%s network=%s elapsed=%s\n%s", *server, *network, elapsed, answer.String())
	return nil
}

func readFrame(reader io.Reader) ([]byte, error) {
	var size uint32
	if err := binary.Read(reader, binary.BigEndian, &size); err != nil {
		return nil, err
	}
	data := make([]byte, int(size))
	_, err := io.ReadFull(reader, data)
	return data, err
}

func writeFrame(writer io.Writer, data []byte) error {
	if err := binary.Write(writer, binary.BigEndian, uint32(len(data))); err != nil {
		return err
	}
	_, err := io.Copy(writer, bytes.NewReader(data))
	return err
}

func newReceipt(transport string, payload []byte, source, destination string) receipt {
	return receipt{Transport: transport, Bytes: len(payload), SHA256: payloadHash(payload), Source: source, Destination: destination}
}

func payloadHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func printJSON(value any) {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}
