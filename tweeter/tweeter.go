package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"image/png"
	"log"
	"os"
	"path"
	"strings"
	"time"

	bpb "github.com/mellowdrifter/bgp_infrastructure/proto/bgpinfo"
	gpb "github.com/mellowdrifter/bgp_infrastructure/proto/grapher"
	"google.golang.org/grpc"
	"gopkg.in/ini.v1"
)

type tweet struct {
	account string
	message string
	media   []byte
}

type srvPort struct {
	server, port string
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

	var grapher srvPort

	logfile := cf.Section("log").Key("logfile").String()
	server := cf.Section("bgpinfo").Key("server").String()
	port := cf.Section("bgpinfo").Key("port").String()
	grapher.server = cf.Section("grapher").Key("server").String()
	grapher.port = cf.Section("grapher").Key("port").String()

	// Set up log file
	f, err := os.OpenFile(logfile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("failed to open logfile: %v\n", err)
	}
	defer f.Close()
	log.SetOutput(f)

	// What action are we going to do.
	action := flag.String("action", "", "an action to perform")
	flag.Parse()

	// gRPC dial to the bgpinfo server.
	conn, err := grpc.Dial(fmt.Sprintf("%s:%s", server, port), grpc.WithInsecure())
	if err != nil {
		log.Fatalf("Unable to dial gRPC server: %s", err)
	}
	defer conn.Close()
	c := bpb.NewBgpInfoClient(conn)

	// Function called will depend on the action required. We only do one action at a time.
	var function func(bpb.BgpInfoClient, srvPort) ([]tweet, error)
	switch *action {
	case "current":
		function = current
	case "movement":
		function = movement
	case "subnets":
		function = subnets
	case "rpki":
		function = rpki
	default:
		log.Fatalf("At least one action must be specified")
	}

	tweets, err := function(c, grapher)
	if err != nil {
		log.Fatalf("Error: %s", err)
	}

	for _, tweet := range tweets {
		fmt.Printf("Account: %s\n", tweet.account)
		fmt.Printf("Message: %s\n", tweet.message)
	}

}

// current grabs the current v4 and v6 table count for tweeting.
func current(c bpb.BgpInfoClient, s srvPort) ([]tweet, error) {
	log.Println("Running current()")
	counts, err := c.GetPrefixCount(context.Background(), &bpb.Empty{})
	if err != nil {
		return nil, err
	}

	// Calculate deltas.
	v4DeltaH := int(counts.GetActive_4() - counts.GetSixhoursv4())
	v6DeltaH := int(counts.GetActive_6() - counts.GetSixhoursv6())
	v4DeltaW := int(counts.GetActive_4() - counts.GetWeekagov4())
	v6DeltaW := int(counts.GetActive_6() - counts.GetWeekagov6())

	// Calculate large subnets percentages
	percentV4 := float32(counts.GetSlash24()) / float32(counts.GetActive_4()) * 100
	percentV6 := float32(counts.GetSlash48()) / float32(counts.GetActive_6()) * 100

	// Formulate updates
	var v4Update, v6Update strings.Builder
	v4Update.WriteString(fmt.Sprintf("I see %d IPv4 prefixes. ", counts.GetActive_4()))
	v4Update.WriteString(deltaMessage(v4DeltaH, v4DeltaW))
	v4Update.WriteString(fmt.Sprintf(". %.2f%% of prefixes are /24.", percentV4))

	v6Update.WriteString(fmt.Sprintf("I see %d IPv6 prefixes. ", counts.GetActive_6()))
	v6Update.WriteString(deltaMessage(v6DeltaH, v6DeltaW))
	v6Update.WriteString(fmt.Sprintf(". %.2f%% of prefixes are /48.", percentV6))

	v4Tweet := tweet{
		account: "bgp4table",
		message: v4Update.String(),
	}
	v6Tweet := tweet{
		account: "bgp6table",
		message: v6Update.String(),
	}

	return []tweet{v4Tweet, v6Tweet}, nil
}

// deltaMessage creates the update message itself. Uses the deltas to formulate the exact message.
func deltaMessage(h, w int) string {
	log.Println("Running deltaMessage()")
	var update strings.Builder
	switch {
	case h == 1:
		update.WriteString("This is 1 more prefix than 6 hours ago ")
	case h == -1:
		update.WriteString("This is 1 less prefix than 6 hours ago ")
	case h < 0:
		update.WriteString(fmt.Sprintf("This is %d fewer prefixes than 6 hours ago ", -h))
	case h > 0:
		update.WriteString(fmt.Sprintf("This is %d more prefixes than 6 hours ago ", h))
	default:
		update.WriteString("No change in the amount of prefixes from 6 hours ago ")

	}

	switch {
	case w == 1:
		update.WriteString("and 1 more than a week ago")
	case w == -1:
		update.WriteString("and 1 less than a week ago")
	case w < 0:
		update.WriteString(fmt.Sprintf("and %d fewer than a week ago", -w))
	case w > 0:
		update.WriteString(fmt.Sprintf("and %d more prefixes than a week ago", w))
	default:
		update.WriteString("and no change in the amount from a week ago")

	}

	return update.String()

}

func subnets(c bpb.BgpInfoClient, s srvPort) ([]tweet, error) {
	log.Println("Running subnets")
	pieData, err := c.GetPieSubnets(context.Background(), &bpb.Empty{})
	if err != nil {
		log.Fatalf("Unable to send proto: %s", err)
	}
	t := time.Now()

	//fmt.Println(proto.MarshalTextString(pieData))

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

	//fmt.Println(proto.MarshalTextString(req))

	// gRPC dial to the grapher
	server := fmt.Sprintf("%s:%s", s.server, s.port)
	conn, err := grpc.Dial(server, grpc.WithInsecure())
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

	v4Tweet := tweet{
		account: "bgp4table",
		message: "temp",
	}
	v6Tweet := tweet{
		account: "bgp6table",
		message: "temp",
	}

	return []tweet{v4Tweet, v6Tweet}, nil

}

func movement(c bpb.BgpInfoClient, s srvPort, p bpb.MovementRequest_TimePeriod) ([]tweet, error) {
	log.Println("Running movement")

	// Get yesterday's date
	y := time.Now().AddDate(0, 0, -1)
	graphData, err := c.GetMovementTotals(context.Background(), &bpb.MovementRequest{Period: p})
	if err != nil {
		return nil, err
	}

	// Determine image title and update message depending on time period given.
	var period string
	var message string
	switch p {
	case bpb.MovementRequest_WEEK:
		period = "week"
		message = "Weekly BGP table movement"
	case bpb.MovementRequest_MONTH:
		period = "month"
		message = "Monthly BGP table movement"
	case bpb.MovementRequest_SIXMONTH:
		period = "6 months"
		message = "BGP table movement for the last 6 months"
	case bpb.MovementRequest_ANNUAL:
		period = "year"
		message = "Annual BGP table movement"
	default:
		return nil, fmt.Errorf("Time Period not set")
	}

	// metadata to create images
	v4Meta := &gpb.Metadata{
		Title:  fmt.Sprintf("IPv4 table movement for %s ending %s", period, y.Format("02-Jan-2006")),
		XAxis:  uint32(12),
		YAxis:  uint32(10),
		Colour: "#238341",
	}
	v6Meta := &gpb.Metadata{
		Title:  fmt.Sprintf("IPv6 table movement for %s ending %s", period, y.Format("02-Jan-2006")),
		XAxis:  uint32(12),
		YAxis:  uint32(10),
		Colour: "#0041A0",
	}

	// repack counts and dates to grapher proto format.
	tt := []*gpb.TotalTime{}
	for _, i := range graphData.GetValues() {
		tt = append(tt, &gpb.TotalTime{
			V4Values: i.GetV4Values(),
			V6Values: i.GetV6Values(),
			Time:     i.GetTime(),
		})
	}
	req := &gpb.LineGraphRequest{
		Metadatas:  []*gpb.Metadata{v4Meta, v6Meta},
		TotalsTime: tt,
		Copyright:  "data by @mellowdrifter | www.mellowd.dev",
	}
	//fmt.Println(proto.MarshalTextString(graphData))

	// gRPC dial to the grapher
	server := fmt.Sprintf("%s:%s", s.server, s.port)
	conn, err := grpc.Dial(server, grpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	g := gpb.NewGrapherClient(conn)

	resp, err := g.GetLineGraph(context.Background(), req)
	if err != nil {
		return nil, err
	}

	// There should be two images, if not something's gone wrong.
	if len(resp.GetImages()) < 2 {
		return nil, fmt.Errorf("Less than two images returned")
	}

	//img, _ := png.Decode(bytes.NewReader(resp.GetImages()[0].GetImage()))

	v4Tweet := tweet{
		account: "bgp4table",
		message: message,
		media:   resp.GetImages()[0].GetImage(),
	}
	v6Tweet := tweet{
		account: "bgp6table",
		message: message,
		media:   resp.GetImages()[1].GetImage(),
	}

	return []tweet{v4Tweet, v6Tweet}, nil

}

func rpki(c bpb.BgpInfoClient, s srvPort) ([]tweet, error) {

	v4Tweet := tweet{
		account: "bgp4table",
		message: "temp",
	}
	v6Tweet := tweet{
		account: "bgp6table",
		message: "temp",
	}

	return []tweet{v4Tweet, v6Tweet}, nil
}
