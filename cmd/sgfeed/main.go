package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"log"
	"os"
	"time"

	"github.com/gen2brain/x264-go"
	"github.com/patroclos/go-conq"
	"github.com/patroclos/go-conq/aid"
	"github.com/patroclos/go-conq/aid/cmdhelp"
	"github.com/patroclos/go-conq/commander"
	"github.com/patroclos/go-conq/getopt"
	"github.com/patroclos/streamgraph"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

var DefaultFrameDuration = time.Millisecond * 33

const (
	FPS    = 15
	Width  = 640
	Height = 420
)

type C = context.Context

func readPackets(rtpSender *webrtc.RTPSender) {
	buf := make([]byte, 1500)
	for {
		if _, _, err := rtpSender.Read(buf); err != nil {
			log.Println(err)
		}
	}

}

func main() {
	com := commander.New(getopt.New(), aid.DefaultHelp)
	err := com.Execute(Root, conq.OSContext())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var Root *conq.Cmd = &conq.Cmd{
	Name:     "sgfeed",
	Run:      run,
	Opts:     conq.Opts{OptPort, OptFrameDuration},
	Args:     conq.Opts{OptAddr},
	Commands: []*conq.Cmd{cmdhelp.New(nil)},
}

var OptAddr = conq.ReqOpt[string]{Name: "address"}
var OptPort = conq.Opt[int]{Name: "port,p"}
var OptFrameDuration = conq.Opt[time.Duration]{Name: "frame-duration,d"}

func run(c conq.Ctx) error {
	go runHub(c, OptAddr.Get(c))
	select {}
}

func runRenderInstance(c conq.Ctx, descr []byte) ([]byte, error) {
	peerConn, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
	})
	if err != nil {
		return nil, fmt.Errorf("failed creating webrtc peer connection: %w", err)
	}

	defer func() {
		if err := peerConn.Close(); err != nil {
			log.Fatal(err)
		}
	}()

	iceConnectedCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	videoTrack, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "video", "pion")
	if err != nil {
		return nil, err
	}

	rtpSender, err := peerConn.AddTrack(videoTrack)
	if err != nil {
		return nil, err
	}
	frameDuration, err := OptFrameDuration.Get(c)
	if err != nil {
		frameDuration = DefaultFrameDuration
	}
	fmt.Fprintf(c.Err, "%s  %v\n", OptFrameDuration.Name, frameDuration)

	imgChan := make(chan image.Image)

	go runStreamgraph(c, imgChan)
	go readPackets(rtpSender)
	go sludgepipe(iceConnectedCtx, c, videoTrack, imgChan)

	peerConn.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		fmt.Fprintf(c.Err, "conn state %q\n", state)
		if state == webrtc.ICEConnectionStateConnected {
			cancel()
		}
	})

	peerConn.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		fmt.Fprintf(c.Err, "peer conn state %q\n", state)
		if state == webrtc.PeerConnectionStateFailed {
			fmt.Fprintf(c.Err, "peer conn failed. exiting")
			os.Exit(0)
		}
	})

	offer := webrtc.SessionDescription{}

	if err != nil {
		return nil, fmt.Errorf("failed reading offer: %w", err)
	}
	fmt.Fprintln(c.Err, offer.Type)

	err = peerConn.SetRemoteDescription(offer)
	if err != nil {
		return nil, err
	}

	answer, err := peerConn.CreateAnswer(nil)
	if err != nil {
		return nil, err
	}

	gatherCompl := webrtc.GatheringCompletePromise(peerConn)
	err = peerConn.SetLocalDescription(answer)
	if err != nil {
		return nil, err
	}

	<-gatherCompl
	fmt.Fprintln(c.Err, "gather compl")

	sdp := *peerConn.LocalDescription()
	b, err := json.Marshal(sdp)
	if err != nil {
		return nil, err
	}

	return []byte(base64.StdEncoding.EncodeToString(b)), nil
}

func sludgepipe(connEstablish C, c conq.Ctx, videoTrack *webrtc.TrackLocalStaticSample, frames <-chan image.Image) {
	<-connEstablish.Done()
	var encoderBuf bytes.Buffer
	encoder, err := x264.NewEncoder(&encoderBuf, &x264.Options{
		Width:     Width,
		Height:    Height,
		FrameRate: FPS,
		Tune:      "film",
		Preset:    "fast",
		Profile:   "high",
		LogLevel:  x264.LogDebug,
	})
	if err != nil {
		panic(err)
	}
	defer encoder.Close()

	noFrame := 0

	framebuffer := x264.NewYCbCr(image.Rect(0, 0, Width, Height))
	for frame := range frames {
		draw.Draw(framebuffer, framebuffer.Bounds(), image.Black, image.ZP, draw.Src)
		for y := 0; y < framebuffer.Bounds().Size().Y; y++ {
			for x := 0; x < framebuffer.Bounds().Size().X; x++ {
				framebuffer.Set(x, y, color.Black)
				framebuffer.Set(x, y, frame.At(x, y))
			}
		}

		noFrame++
		fmt.Fprintf(c.Err, "[%d] coding image %+v\n", noFrame, frame.Bounds().Size())
		fmt.Fprintf(c.Err, "framebuffer: %v\n", framebuffer.YCbCrAt(0, 0))
		(&encoderBuf).Reset()
		encoder.Encode(framebuffer)

		b, err := io.ReadAll(&encoderBuf)
		if err != nil {
			panic(err)
		}

		smpl := media.Sample{Data: b, Duration: time.Second / FPS, Timestamp: time.Now()}
		if err := videoTrack.WriteSample(smpl); err != nil {
			panic(err)
		}
		fmt.Fprintf(c.Err, "[%d] written %d bytes via media sample (%v)\n", noFrame, len(smpl.Data), smpl.Duration)

		fmt.Fprintf(c.Err, "wrote frame %d (h264)\n", noFrame)
	}

}

func runStreamgraph(c conq.Ctx, img chan<- image.Image) {
	gra := streamgraph.New()
	testLog := make(chan streamgraph.LogEvent, 1)
	go func() {
		testLog <- streamgraph.Log(streamgraph.LogDebug, "hello world")
		<-time.After(14 * time.Second)
		fmt.Fprintf(os.Stderr, "AAAAAAAAAAAAAAA")
		testLog <- streamgraph.Log(streamgraph.LogDebug, "hel HEL hel HEL hel")
		<-time.After(14 * time.Second)
		fmt.Fprintf(os.Stderr, "AAAAAAAAAAAAAAA")
		testLog <- streamgraph.Log(streamgraph.LogDebug, "o o O oO O")
		<-time.After(14 * time.Second)
		fmt.Fprintf(os.Stderr, "AAAAAAAAAAAAAAA")
		testLog <- streamgraph.Log(streamgraph.LogDebug, "ohai")
	}()
	egr := &streamgraph.VideoEgress{
		No:  &streamgraph.Node{Name: "eventlog"},
		Fps: 30,
		Maps: []*streamgraph.Mapping{
			{Rect: image.Rect(0, 0, Width, Height), Render: streamgraph.RenderLog(testLog)},
		},
	}
	gra.SetNode(egr.No)

	req := &streamgraph.RecordRequest{
		First: 0,
		Last:  60 * 60 * 60 * 24,
		Sink:  img,
	}
	_, err := egr.Record(req)
	if err != nil {
		return
	}
}
