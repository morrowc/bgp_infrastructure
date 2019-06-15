package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"path"
	"time"

	"github.com/golang/protobuf/proto"
	"github.com/mellowdrifter/bgp_infrastructure/proto/bgpinfo"
	bpb "github.com/mellowdrifter/bgp_infrastructure/proto/bgpinfo"
	gpb "github.com/mellowdrifter/bgp_infrastructure/proto/grapher"
	"google.golang.org/grpc"
	"gopkg.in/ini.v1"
)

type tweet struct {
	message string
	image   image.Image
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

	logfile := cf.Section("grpc").Key("logfile").String()
	server := cf.Section("grpc").Key("server").String()
	port := cf.Section("grpc").Key("port").String()

	// Set up log file
	f, err := os.OpenFile(logfile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("failed to open logfile: %v\n", err)
	}
	defer f.Close()
	log.SetOutput(f)

	//getPrivateASLeak(as)

	// gRPC dial and send data
	conn, err := grpc.Dial(fmt.Sprintf("%s:%s", server, port), grpc.WithInsecure())
	if err != nil {
		log.Fatalf("Unable to dial gRPC server: %s", err)
	}
	defer conn.Close()
	c := bpb.NewBgpInfoClient(conn)

	//pie(c)
	graph(c)

}

func pie(c bgpinfo.BgpInfoClient) {
	pieData, err := c.GetPieSubnets(context.Background(), &bpb.Empty{})
	if err != nil {
		log.Fatalf("Unable to send proto: %s", err)
	}
	t := time.Now()

	fmt.Println(proto.MarshalTextString(pieData))

	v4Colours := []string{"burlywood", "lightgreen", "lightskyblue", "lightcoral", "gold"}
	v6Colours := []string{"lightgreen", "burlywood", "lightskyblue", "violet", "linen", "lightcoral", "gold"}
	v4Lables := []string{"/19-/21", "/16-/18", "/22", "/23", "/24"}
	v6Lables := []string{"/32", "/44", "/40", "/36", "/29", "The Rest", "/48"}
	v4Meta := &gpb.Metadata{
		Title:   fmt.Sprintf("Current prefix range distribution for IPv4 (%s)", t.Format("02-Jan-2006")),
		XAxis:   uint32(12),
		YAxis:   uint32(10),
		Colours: v4Colours,
		Labels:  v4Lables,
	}
	v6Meta := &gpb.Metadata{
		Title:   fmt.Sprintf("Current prefix range distribution for IPv6 (%s)", t.Format("02-Jan-2006")),
		XAxis:   uint32(12),
		YAxis:   uint32(10),
		Colours: v6Colours,
		Labels:  v6Lables,
	}

	v4Subnets := []uint32{
		pieData.GetMasks().GetV4_19() + pieData.GetMasks().GetV4_20() + pieData.GetMasks().GetV4_21(),
		pieData.GetMasks().GetV4_16() + pieData.GetMasks().GetV4_17() + pieData.GetMasks().GetV4_18(),
		pieData.GetMasks().GetV4_22(),
		pieData.GetMasks().GetV4_23(),
		pieData.GetMasks().GetV4_24(),
	}
	v6Subnets := []uint32{
		pieData.GetMasks().GetV6_32(),
		pieData.GetMasks().GetV6_44(),
		pieData.GetMasks().GetV6_40(),
		pieData.GetMasks().GetV6_36(),
		pieData.GetMasks().GetV6_29(),
		pieData.GetV6Total() - pieData.GetMasks().GetV6_32() - pieData.GetMasks().GetV6_44() -
			pieData.GetMasks().GetV6_40() - pieData.GetMasks().GetV6_36() - pieData.GetMasks().GetV6_29() -
			pieData.GetMasks().GetV6_48(),
		pieData.GetMasks().GetV6_48(),
	}

	req := &gpb.PieChartRequest{
		Metadatas: []*gpb.Metadata{v4Meta, v6Meta},
		Subnets: &gpb.SubnetFamily{
			V4Values: v4Subnets,
			V6Values: v6Subnets,
		},
		Copyright: "data by @mellowdrifter | www.mellowd.dev",
	}

	fmt.Println(proto.MarshalTextString(req))

	// gRPC dial and send data
	conn, err := grpc.Dial("127.0.0.1:7180", grpc.WithInsecure())
	if err != nil {
		log.Fatalf("Unable to dial gRPC server: %s", err)
	}
	defer conn.Close()
	g := gpb.NewGrapherClient(conn)

	images, err := g.GetPieChart(context.Background(), req)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	for _, pie := range images.GetImages() {
		fmt.Printf("Title is %s\n", pie.GetTitle())
		img, err := png.Decode(bytes.NewReader(pie.GetImage()))
		if err != nil {
			log.Fatalf("Error: %v", err)
		}
		out, _ := os.Create(fmt.Sprintf("%s.png", pie.GetTitle()))
		defer out.Close()

		err = png.Encode(out, img)
		if err != nil {
			log.Fatalf("Err: %v", err)
		}

	}

}

func graph(c bgpinfo.BgpInfoClient) {
	// Gets yesterday's date
	y := time.Now().AddDate(0, 0, -1)
	graphData, err := c.GetMovementTotals(context.Background(), &bpb.MovementRequest{Period: bpb.MovementRequest_MONTH})
	if err != nil {
		log.Fatalf("Error: %v", err)
	}
	v4Meta := &gpb.Metadata{
		Title:  fmt.Sprintf("Ipv4 table movement for week ending %s", y.Format("02-Jan-2006")),
		XAxis:  uint32(12),
		YAxis:  uint32(10),
		Colour: uint32(100),
	}
	v6Meta := &gpb.Metadata{
		Title:  fmt.Sprintf("Ipv6 table movement for week ending %s", y.Format("02-Jan-2006")),
		XAxis:  uint32(12),
		YAxis:  uint32(10),
		Colour: uint32(200),
	}
	tt := gpb.TotalTime{}
	req := *gpb.LineGraphRequest{
		Metadatas: []*gpb.Metadata{v4Meta, v6Meta},
		Copyright: "data by @mellowdrifter | www.mellowd.dev",
	}
	fmt.Println(proto.MarshalTextString(graphData))

}
