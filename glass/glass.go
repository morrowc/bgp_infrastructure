package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path"
	"sync"
	"time"

	"google.golang.org/grpc/keepalive"

	cli "github.com/mellowdrifter/bgp_infrastructure/clidecode"
	com "github.com/mellowdrifter/bgp_infrastructure/common"
	bpb "github.com/mellowdrifter/bgp_infrastructure/proto/bgpsql"
	pb "github.com/mellowdrifter/bgp_infrastructure/proto/glass"
	"google.golang.org/grpc"
	"gopkg.in/ini.v1"
)

type server struct {
	router cli.Decoder
	mu     *sync.RWMutex
	bsql   *grpc.ClientConn
	cache
}

func main() {
	// load in config
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	path := fmt.Sprintf("%s/config.ini", path.Dir(exe))
	cf, err := ini.Load(path)
	if err != nil {
		log.Fatalf("failed to read config file: %v\n", err)
	}

	logfile := cf.Section("log").Key("logfile").String()

	// Set up log file
	f, err := os.OpenFile(logfile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("failed to open logfile: %v\n", err)
	}
	defer f.Close()
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.SetOutput(f)

	// TODO: Bird2 for now. Could change
	var router cli.Bird2Conn

	conn, err := dialGRPC(cf.Section("bgpsql").Key("server").String())
	if err != nil {
		log.Fatalf("Unable to dial gRPC server: %v", err)
	}

	glassServer := &server{
		router: router,
		mu:     &sync.RWMutex{},
		bsql:   conn,
		cache:  getNewCache(),
	}

	// set up gRPC server
	log.Printf("Listening on port %d\n", 7181)
	lis, err := net.Listen("tcp", ":7181")
	if err != nil {
		log.Fatalf("Failed to bind: %v", err)
	}
	grpcServer := grpc.NewServer()
	pb.RegisterLookingGlassServer(grpcServer, glassServer)

	go glassServer.clearCache()

	grpcServer.Serve(lis)

}

func dialGRPC(srv string) (*grpc.ClientConn, error) {
	// Set keepalive on the client
	var kacp = keepalive.ClientParameters{
		Time:    10 * time.Second, // send pings every 10 seconds if there is no activity
		Timeout: 3 * time.Second,  // wait 3 seconds for ping ack before considering the connection dead
	}

	log.Printf("Dialling %s\n", srv)
	return grpc.Dial(
		srv,
		grpc.WithInsecure(),
		grpc.WithKeepaliveParams(kacp),
		grpc.WithBlock(),
	)
}

// TotalAsns will return the total number of course ASNs.
func (s *server) TotalAsns(ctx context.Context, e *pb.Empty) (*pb.TotalAsnsResponse, error) {
	log.Printf("Running TotalAsns")

	as, err := s.router.GetTotalSourceASNs()
	if err != nil {
		log.Printf("Error: %v", err)
		return nil, err
	}

	return &pb.TotalAsnsResponse{
		As4:     as.As4,
		As6:     as.As6,
		As10:    as.As10,
		As4Only: as.As4Only,
		As6Only: as.As6Only,
		AsBoth:  as.AsBoth,
	}, nil

}

// Origin will return the origin ASN for the active route.
func (s *server) Origin(ctx context.Context, r *pb.OriginRequest) (*pb.OriginResponse, error) {
	log.Printf("Running Origin")

	ip, err := com.ValidateIP(r.GetIpAddress().GetAddress())
	if err != nil {
		log.Printf("Error: %v", err)
		return nil, err
	}

	// check local cache
	origin, ok := s.checkOriginCache(ip.String())
	if ok {
		return &pb.OriginResponse{
			OriginAsn: origin,
			Exists:    true,
		}, nil
	}

	origin, exists, err := s.router.GetOriginFromIP(ip)
	if err != nil {
		log.Printf("Error: %v", err)
		return nil, err
	}
	if !exists {
		return nil, nil
	}

	s.updateOriginCache(ip, origin)

	return &pb.OriginResponse{
		OriginAsn: origin,
		Exists:    exists,
	}, nil
}

// Totals will return the current IPv4 and IPv6 FIB.
// Grabs from database as it's updated every 5 minutes.
func (s *server) Totals(ctx context.Context, e *pb.Empty) (*pb.TotalResponse, error) {
	log.Printf("Running Totals")

	// check local cache first
	if s.checkTotalCache() {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return &pb.TotalResponse{
			Active_4: s.totalCache.v4,
			Active_6: s.totalCache.v6,
			Time:     s.totalCache.time,
		}, nil
	}

	stub := bpb.NewBgpInfoClient(s.bsql)
	totals, err := stub.GetPrefixCount(ctx, &bpb.Empty{})
	if err != nil {
		log.Printf("No connection to bgpsql RPC server")
		return nil, err
	}

	s.updateTotalCache(totals)

	return &pb.TotalResponse{
		Active_4: totals.GetActive_4(),
		Active_6: totals.GetActive_6(),
		Time:     totals.GetTime(),
	}, nil

}

// Aspath returns a list of ASNs for an IP address.
func (s *server) Aspath(ctx context.Context, r *pb.AspathRequest) (*pb.AspathResponse, error) {
	log.Printf("Running Aspath")

	ip, err := com.ValidateIP(r.GetIpAddress().GetAddress())
	if err != nil {
		log.Printf("Error: %v", err)
		return nil, err
	}

	// check local cache
	a, st, ok := s.checkASPathCache(ip.String())
	if ok {
		return &pb.AspathResponse{
			Asn:    a,
			Set:    st,
			Exists: true,
		}, nil
	}

	paths, exists, err := s.router.GetASPathFromIP(ip)
	if err != nil {
		log.Printf("Error: %v", err)
		return nil, err
	}

	// IP route may not exist. Return no error, but not existing either.
	if !exists {
		return nil, nil
	}

	// Repackage into proto
	var p = make([]*pb.Asn, 0, len(paths.Path))
	for _, v := range paths.Path {
		p = append(p, &pb.Asn{
			Asplain: v,
			Asdot:   com.ASPlainToASDot(v),
		})
	}

	var set = make([]*pb.Asn, 0, len(paths.Set))
	if len(set) > 0 {
		for _, v := range paths.Set {
			set = append(set, &pb.Asn{
				Asplain: v,
				Asdot:   com.ASPlainToASDot(v),
			})
		}
	}

	// update the cache
	s.updateASPathCache(ip, p, set)

	return &pb.AspathResponse{
		Asn:    p,
		Set:    set,
		Exists: exists,
	}, nil
}

// Route returns the primary active RIB entry for the requested IP.
func (s *server) Route(ctx context.Context, r *pb.RouteRequest) (*pb.RouteResponse, error) {
	log.Printf("Running Route")

	ip, err := com.ValidateIP(r.GetIpAddress().GetAddress())
	if err != nil {
		return nil, errors.New("Unable to validate IP")
	}

	// check local cache first
	ipnetcache, ok := s.checkRouteCache(ip.String())
	if ok {
		return &pb.RouteResponse{
			IpAddress: &ipnetcache,
			Exists:    true,
		}, nil
	}

	ipnet, exists, err := s.router.GetRoute(ip)
	if err != nil {
		log.Printf("Error: %v", err)
		return nil, err
	}
	if !exists {
		return nil, nil
	}

	mask, _ := ipnet.Mask.Size()
	ipaddr := &pb.IpAddress{
		Address: ipnet.IP.String(),
		Mask:    uint32(mask),
	}

	// cache the result
	s.updateRouteCache(ip, ipaddr)

	return &pb.RouteResponse{
		IpAddress: ipaddr,
		Exists:    exists,
	}, nil
}

// Asname will return the registered name of the ASN. As this isn't in bird directly, will need
// to speak to bgpsql to get information from the database.
func (s *server) Asname(ctx context.Context, r *pb.AsnameRequest) (*pb.AsnameResponse, error) {
	//return nil, grpc.Errorf(codes.Unimplemented, "RPC not yet implemented")
	log.Printf("Running Asname")

	// check local cache first
	n, l, ok := s.checkASNCache(r.GetAsNumber())
	if ok {
		return &pb.AsnameResponse{
			AsName: n,
			Locale: l,
			Exists: true,
		}, nil
	}

	number := bpb.GetAsnameRequest{AsNumber: r.GetAsNumber()}

	stub := bpb.NewBgpInfoClient(s.bsql)
	name, err := stub.GetAsname(ctx, &number)
	if err != nil {
		log.Printf("No connection to bgpsql RPC server")
		return nil, err
	}

	// Cache the result for next time
	s.updateASNCache(name.GetAsName(), name.GetAsLocale(), r.GetAsNumber())

	return &pb.AsnameResponse{
		AsName: name.GetAsName(),
		Locale: name.GetAsLocale(),
		Exists: name.Exists,
	}, nil

}

// Roa will check the ROA status of a prefix.
func (s *server) Roa(ctx context.Context, r *pb.RoaRequest) (*pb.RoaResponse, error) {
	log.Printf("Running Roa")

	ip, err := com.ValidateIP(r.GetIpAddress().GetAddress())
	if err != nil {
		log.Printf("Error: %v", err)
		return nil, err
	}

	// In oder to check ROA, I first need the FIB entry for the IP address.
	ipnet, exists, err := s.router.GetRoute(ip)
	if err != nil {
		log.Printf("Error: %v", err)
		return nil, err
	}
	// TODO: Not sure if I should check cache before?
	// or getroute should be cached itself
	if !exists {
		return nil, fmt.Errorf("No route exists for %s, so unable to check ROA status", ip.String())
	}

	// check local cache
	roa, ok := s.checkROACache(ipnet)
	if ok {
		return roa, nil
	}

	status, exists, err := s.router.GetROA(ipnet)
	if err != nil {
		log.Printf("Error: %v", err)
		return nil, err
	}

	// Check for an existing ROA
	// I've set local preference on all routes to make this easier to determine:
	// 200 = ROA_VALID
	// 100 = ROA_UNKNOWN
	//  50 = ROA_INVALID
	statuses := map[int]pb.RoaResponse_ROAStatus{
		cli.RUnknown: pb.RoaResponse_UNKNOWN,
		cli.RInvalid: pb.RoaResponse_INVALID,
		cli.RValid:   pb.RoaResponse_VALID,
	}

	mask, _ := ipnet.Mask.Size()
	resp := &pb.RoaResponse{
		IpAddress: &pb.IpAddress{
			Address: ipnet.IP.String(),
			Mask:    uint32(mask),
		},
		Status: statuses[status],
		Exists: exists,
	}
	// update cache
	s.updateROACache(ipnet, resp)

	return resp, nil
}

func (s *server) Sourced(ctx context.Context, r *pb.SourceRequest) (*pb.SourceResponse, error) {
	log.Printf("Running Sourced")
	defer com.TimeFunction(time.Now(), "Sourced")

	if !com.ValidateASN(r.GetAsNumber()) {
		return nil, fmt.Errorf("Invalid AS number")
	}

	// check local cache first
	p, ok := s.checkSourcedCache(r.GetAsNumber())
	if ok {
		return &pb.SourceResponse{
			IpAddress: p.prefixes,
			Exists:    true,
			V4Count:   p.v4,
			V6Count:   p.v6,
		}, nil
	}

	v4, err := s.router.GetIPv4FromSource(r.GetAsNumber())
	if err != nil {
		return nil, fmt.Errorf("Error on getting IPv4 from source: %w", err)
	}

	var prefixes = make([]*pb.IpAddress, 0, len(v4))
	for _, v := range v4 {
		mask, _ := v.Mask.Size()
		prefixes = append(prefixes, &pb.IpAddress{
			Address: v.IP.String(),
			Mask:    uint32(mask),
		})
	}

	v6, err := s.router.GetIPv6FromSource(r.GetAsNumber())
	if err != nil {
		return nil, fmt.Errorf("Error on getting IPv6 from source: %w", err)
	}

	for _, v := range v6 {
		mask, _ := v.Mask.Size()
		prefixes = append(prefixes, &pb.IpAddress{
			Address: v.IP.String(),
			Mask:    uint32(mask),
		})
	}

	// Update the local cache
	s.updateSourcedCache(prefixes, uint32(len(v4)), uint32(len(v6)), r.GetAsNumber())

	// No prefixes will return empty, but no error
	if len(prefixes) == 0 {
		return &pb.SourceResponse{}, nil
	}

	return &pb.SourceResponse{
		IpAddress: prefixes,
		Exists:    true,
		V4Count:   uint32(len(v4)),
		V6Count:   uint32(len(v6)),
	}, nil
}
