package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"os"
	"path"

	"github.com/golang/protobuf/proto"
	pb "github.com/mellowdrifter/bgp_infrastructure/proto/bgpinfo"
	"google.golang.org/grpc"
	ini "gopkg.in/ini.v1"
)

type server struct{}

type config struct {
	port     string
	priority uint
	peer     string
	logfile  string
	db       dbinfo
}

type dbinfo struct {
	user, pass, dbname string
}

var cfg config
var db *sql.DB

// init is here to read all the config.ini options. Ensure they are correct.
func init() {

	// read config
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	path := fmt.Sprintf("%s/config.ini", path.Dir(exe))
	cf, err := ini.Load(path)
	if err != nil {
		log.Fatalf("failed to read config file: %v\n", err)
	}
	cfg.port = fmt.Sprintf(":" + cf.Section("grpc").Key("port").String())
	cfg.logfile = fmt.Sprintf(cf.Section("log").Key("file").String())
	cfg.db.dbname = cf.Section("sql").Key("database").String()
	cfg.db.user = cf.Section("sql").Key("username").String()
	cfg.db.pass = cf.Section("sql").Key("password").String()

}

func main() {
	// Set up log file
	f, err := os.OpenFile(cfg.logfile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("failed to open logfile: %v\n", err)
	}
	defer f.Close()
	log.SetOutput(f)

	// Create sql handle and test database connection
	sqlserver := fmt.Sprintf("%s:%s@tcp(127.0.0.1:3306)/%s", cfg.db.user, cfg.db.pass, cfg.db.dbname)
	db, err = sql.Open("mysql", sqlserver)
	if err != nil {
		log.Fatalf("can't open database. Got %v", err)
	}
	err = db.Ping()
	if err != nil {
		log.Fatalf("can't ping database. Got %v", err)
	}
	defer db.Close()

	// set up gRPC server
	log.Printf("Listening on port %s\n", cfg.port)
	lis, err := net.Listen("tcp", cfg.port)
	if err != nil {
		log.Fatalf("Failed to bind: %v", err)
	}
	grpcServer := grpc.NewServer()
	pb.RegisterBgpInfoServer(grpcServer, &server{})

	grpcServer.Serve(lis)
}

func (s *server) AddLatest(ctx context.Context, v *pb.Values) (*pb.Result, error) {
	// Receive the latest BGP info updates and add to the database
	log.Println("Received an update")
	log.Println(proto.MarshalTextString(v))

	// get correct struct
	update := repack(v)

	// update database
	err := add(update)
	if err != nil {
		return &pb.Result{}, err
	}

	return &pb.Result{
		Success: true,
	}, nil
}

func (s *server) GetPrefixCount(ctx context.Context, v *pb.Empty) (*pb.PrefixCountResponse, error) {
	// Pull prefix counts for tweeting. Latest, 6 hours ago, and a week ago.
	log.Println("Running GetPrefixCount")

	res, err := getPrefixCountHelper()
	if err != nil {
		return &pb.PrefixCountResponse{}, err
	}

	return res, nil
}
