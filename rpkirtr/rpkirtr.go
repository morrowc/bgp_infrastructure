package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "net/http/pprof"
)

const (
	//cacheurl = "https://rpki.cloudflare.com/rpki.json"
	cache   = "data/rpki.json"
	logfile = "/var/log/rpkirtr.log"

	// Each region will just be an enum.
	afrinic rir = 0
	apnic   rir = 1
	arin    rir = 2
	lacnic  rir = 3
	ripe    rir = 4

	// refresh is the amount of seconds to wait until a new json is pulled.
	refresh = 4 * time.Minute
	//refresh = 10 * time.Second

	// 8282 is the RFC port for RPKI-RTR
	port = 8282
	loc  = "localhost"
)

var cacheSerial uint32 = 0

// enum used for RIRs
type rir int

// jsonroa is a struct to push the cloudflare ROA data into.
type jsonroa struct {
	Prefix string  `json:"prefix"`
	Mask   float64 `json:"maxLength"`
	ASN    string  `json:"asn"`
	RIR    string  `json:"ta"`
}

// Converted ROA struct with all the details.
type roa struct {
	Prefix  string
	MinMask int
	MaxMask int
	ASN     int
	RIR     rir
}

// rpkiResponse, metadata, and roas are all used to unmarshal the json file.
type rpkiResponse struct {
	metadata `json:"metadata"`
	roas
}
type metadata struct {
	Generated float64 `json:"generated"`
	Valid     float64 `json:"valid"`
}
type roas struct {
	Roas []jsonroa `json:"roas"`
}

// Server is our main thing
type Server struct {
	roas []roa
	net  net.Conn
}

func main() {

	// set up log file
	f, err := os.OpenFile(logfile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatal(fmt.Errorf("failed to open logfile: %w", err))
	}
	defer f.Close()
	log.SetOutput(f)

	go func() {
		log.Println(http.ListenAndServe("localhost:6060", nil))
	}()

	dir, err := os.Getwd()
	if err != nil {
		log.Fatal(fmt.Errorf("unable to get current working directory: %w", err))
	}

	roaFile := path.Join(dir, cache)
	log.Printf("Downloading %s\n", roaFile)

	// This is our server itself
	var thing Server

	// Keep ROAs updated
	go thing.readROAs(roaFile)
	time.Sleep(time.Second * 5)

	// Actually do something
	// add := fmt.Sprintf("%s:%d", loc, port)
	l, err := net.ListenTCP("tcp", &net.TCPAddr{Port: port})
	if err != nil {
		fmt.Printf("Unable to start server: %w", err)
		os.Exit(1)
	}
	defer l.Close()
	for {
		conn, err := l.AcceptTCP()
		if err != nil {
			fmt.Println(err)
			continue
		}
		//thing.handleQuery()
		go startSession(conn, thing.roas)
	}

}

// readROAs will update the server struct with the current list of ROAs
func (s *Server) readROAs(f string) {
	for {
		cacheSerial++
		roas, err := readROAs(f)
		if err != nil {
			log.Fatal(err)
		}
		s.roas = roas
		fmt.Println("roas updated")
		time.Sleep(refresh)
	}

}

// readROAs will read the current ROAs into memory.
func readROAs(file string) ([]roa, error) {

	f, err := ioutil.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("unable to read file: %w", err)
	}

	rirs := map[string]rir{
		"Cloudflare - AFRINIC": afrinic,
		"Cloudflare - ARIN":    arin,
		"Cloudflare - APNIC":   apnic,
		"Cloudflare - LACNIC":  lacnic,
		"Cloudflare - RIPE":    ripe,
	}

	var r rpkiResponse
	json.Unmarshal(f, &r)

	// We know how many ROAs we have, so we can add that capacity directly
	roas := make([]roa, 0, len(r.roas.Roas))

	rxp := regexp.MustCompile(`(.*)/(.*)`)

	for _, r := range r.roas.Roas {
		prefix := rxp.FindStringSubmatch(r.Prefix)
		roas = append(roas, roa{
			Prefix:  prefix[1],
			MinMask: stringToInt(prefix[2]),
			MaxMask: int(r.Mask),
			ASN:     asnToInt(r.ASN),
			RIR:     rirs[r.RIR],
		})

	}

	return roas, nil

}

// stringToInt does inline convertions and logs errors, instead of panicing.
func stringToInt(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		log.Printf("Unable to convert %s to int", s)
		return 0
	}

	return n
}

// The Cloudflare JSON prepends AS to all numbers. Need to remove it here.
func asnToInt(a string) int {
	n, err := strconv.Atoi(a[2:])
	if err != nil {
		log.Printf("Unable to convert ASN %s to int", a)
		return 0
	}

	return n
}

func (s *Server) handleQuery() {
	fmt.Printf("Incoming from %s\n", s.net.RemoteAddr().String())
	defer s.net.Close()

	buf := new(bytes.Buffer)
	readBuf := make([]byte, 1024)

	for {
		dataLen, err := s.net.Read(readBuf)
		if err != nil {
			if err == io.EOF {
				fmt.Println("Connection closed by client!")
				break
			}
		}
		buf.Write(readBuf[:dataLen])

		fmt.Printf("*** %v\n", buf)

	}
}

func startSession(conn *net.TCPConn, roas []roa) {
	fmt.Printf("Incoming from %s\n", conn.RemoteAddr().String())

	cpdu := cacheResponsePDU{
		sessionID: uint16(123),
	}
	cpdu.serialize(conn)

	for _, roa := range roas {
		IPAddress := net.ParseIP(roa.Prefix)
		// TODO put ipv4/ipv6 signal in when creating the ROAs
		switch strings.Contains(roa.Prefix, ":") {
		case true:
			ppdu := ipv6PrefixPDU{
				flags:  uint8(1),
				min:    uint8(roa.MinMask),
				max:    uint8(roa.MaxMask),
				prefix: IPAddress.To16(),
				asn:    uint32(roa.ASN),
			}
			ppdu.serialize(conn)
		case false:
			ppdu := ipv4PrefixPDU{
				flags:  uint8(1),
				min:    uint8(roa.MinMask),
				max:    uint8(roa.MaxMask),
				prefix: IPAddress.To4(),
				asn:    uint32(roa.ASN),
			}
			ppdu.serialize(conn)
		}
	}
	fmt.Printf("Finished sending all IPv4 prefixes")
	epdu := endOfDataPDU{
		sessionID: uint16(123),
		serial:    cacheSerial,
		refresh:   uint32(900),
		retry:     uint32(30),
		expire:    uint32(171999),
	}
	epdu.serialize(conn)

}
