package streamgraph

import (
	"image"
	"image/color"
	"image/draw"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/inconsolata"
	"golang.org/x/image/math/fixed"
)

type NodeType int

const (
	NodeLog NodeType = iota
	NodeGauge
)

func New() *G {
	return new(G)
}

type G struct {
	Roots  []*Node
	Nodes  map[string]*Node
	lastId int64
}

func (g *G) nextId() int64 {
	g.lastId++
	return g.lastId
}

func (g *G) SetNode(n *Node) {
	if g.Nodes == nil {
		g.Nodes = map[string]*Node{n.Name: n}
		return
	}
	g.Nodes[n.Name] = n
}

type Node struct {
	Id   int64
	Name string
	Typ  NodeType
}

type RecordRequest struct {
	Sink        chan<- image.Image
	First, Last int
	Gr          *G
	Node        *Node
	Recording   *Recording
}

type Recording struct {
	Elapsed       time.Duration
	FramesWritten int64
	Errs          []error
}

type Recorder interface {
	Record(*RecordRequest) (*Recording, error)
}

// exit-node
type VideoEgress struct {
	No   *Node
	Maps []*Mapping
	Fps  float64
}

func sizeOfMaps(maps []*Mapping) (rect image.Rectangle) {
	for _, m := range maps {
		rect = rect.Union(m.Rect)
	}
	return
}

func (ve *VideoEgress) Record(req *RecordRequest) (record *Recording, err error) {
	record = new(Recording)
	dim := sizeOfMaps(ve.Maps)
	buffers := make([]*image.RGBA, len(ve.Maps))
	for i, m := range ve.Maps {
		buffers[i] = image.NewRGBA(m.Rect)
	}

	start := time.Now()
	defer func() {
		record.Elapsed = time.Now().Sub(start)
	}()

	for frame := 0; frame < req.Last; frame++ {
		img := image.NewRGBA(dim)
		for y := 0; y < dim.Dy(); y++ {
			for x := 0; x < dim.Dx(); x++ {
				img.Set(x, y, color.Gray16{0x3333})
			}
		}

		smp := sample{t: time.Now(), rel: float64(frame) / ve.Fps}
		barrier := start.Add(time.Duration(smp.rel) * time.Second)
		for !time.Now().After(barrier) {
			<-time.After(barrier.Sub(time.Now()))
		}

		var wg sync.WaitGroup
		wg.Add(len(ve.Maps))
		for i, m := range ve.Maps {
			go func(buf *image.RGBA, m *Mapping) {
				defer wg.Done()
				m.Render(smp, buf)
				draw.Draw(img, m.Rect, buf, image.Point{m.Rect.Min.X, m.Rect.Min.Y}, draw.Over)
			}(buffers[i], m)
		}
		wg.Wait()

		if req.Sink != nil {
			req.Sink <- img
			record.FramesWritten++
		}
	}
	return
}

type Mapping struct {
	Rect   image.Rectangle
	Render func(Sample, *image.RGBA)
}

type Sample interface {
	Sample() (t time.Time, normal float64)
}

type sample struct {
	t   time.Time
	rel float64
}

func (s sample) Sample() (time.Time, float64) { return s.t, s.rel }

type LogLevel int

const (
	LogTrace LogLevel = iota
	LogDebug
	LogInfo
	LogWarn
	LogError
)

func Log(lvl LogLevel, msg string) LogEvent {
	return evt{lvl: lvl, msg: msg}
}

type evt struct {
	lvl LogLevel
	msg string
}

func (e evt) LogEvent() (string, LogLevel) {
	return e.msg, e.lvl
}

type LogEvent interface {
	LogEvent() (msg string, level LogLevel)
}

func RenderLog(s <-chan LogEvent) func(Sample, *image.RGBA) {
	return (&renderLog{src: s}).Render
}

type renderLog struct {
	src     <-chan LogEvent
	entries []struct {
		msg string
		lvl LogLevel
	}
}

func (l *renderLog) Render(s Sample, buf *image.RGBA) {
	select {
	case s := <-l.src:
		msg, lvl := s.LogEvent()
		l.entries = append(l.entries, struct {
			msg string
			lvl LogLevel
		}{msg, lvl})
	default:
		return
	}

	for y := 0; y < buf.Rect.Size().Y; y++ {
		for x := 0; x < buf.Rect.Size().X; x++ {
			buf.Set(x, y, color.RGBA{22, 0, 22, 0xff})
		}
	}

	draw := &font.Drawer{
		Dst:  buf,
		Src:  image.White,
		Face: inconsolata.Regular8x16,
		Dot:  fixed.Point26_6{X: 0, Y: fixed.Int26_6(buf.Bounds().Dy())},
	}
	msg := l.entries[len(l.entries)-1].msg
	draw.DrawString(msg)
}
