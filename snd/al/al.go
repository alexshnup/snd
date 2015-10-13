package al

import (
	"fmt"
	"log"
	"math"
	"time"

	"dasa.cc/piano/snd"

	"golang.org/x/mobile/exp/audio/al"
)

var hwa *openal

// Buffer provides adaptive buffering for real-time responses that outpace
// openal's ability to report a buffer as processed.
// Constant-time synchronization is left up to the caller.
// TODO consider naming SourceBuffers and embedding
type Buffer struct {
	src  al.Source
	bufs []al.Buffer // intrinsic to src
	size int         // size of buffers returned by Get
	idx  int         // last known index still processing or ready for reuse
}

// Get returns a slice of buffers of length b.size ready to receive data and be queued.
// If b.src reports processing at-least as many buffers as b.size, buffers up to b.size
// will be unqueued for reuse. Otherwise, new buffers will be generated.
func (b *Buffer) Get() (bufs []al.Buffer) {
	if proc := int(b.src.BuffersProcessed()); proc >= b.size {
		// advance by size, BuffersProcessed will report that many less next time.
		bufs = b.bufs[b.idx : b.idx+b.size]
		b.src.UnqueueBuffers(bufs)
		if code := al.Error(); code != 0 {
			log.Printf("snd/al: unqueue buffers failed [err=%v]\n", code)
		}
		b.idx = (b.idx + b.size) % len(b.bufs)
	} else {
		// make more buffers to fill data regardless of what openal says about processed.
		bufs = al.GenBuffers(b.size)
		if code := al.Error(); code != 0 {
			log.Printf("snd/al: generate buffers failed [err=%v]", code)
		}
		b.bufs = append(b.bufs, bufs...)
	}
	return bufs
}

type openal struct {
	source al.Source
	buf    *Buffer

	format uint32
	in     snd.Sound
	out    []byte

	quit chan struct{}

	underruns uint64
	ticktime  time.Duration
	tickcount uint64

	tc uint64
}

func OpenDevice(buflen int) error {
	if err := al.OpenDevice(); err != nil {
		return fmt.Errorf("snd/al: open device failed: %s", err)
	}
	if buflen == 0 || buflen&(buflen-1) != 0 {
		return fmt.Errorf("snd/al: buflen(%v) not a power of 2", buflen)
	}
	hwa = &openal{buf: &Buffer{size: buflen}}
	return nil
}

func CloseDevice() error {
	al.DeleteBuffers(hwa.buf.bufs)
	al.DeleteSources(hwa.source)
	al.CloseDevice()
	hwa = nil
	return nil
}

func AddSource(in snd.Sound) error {
	switch in.Channels() {
	case 1:
		hwa.format = al.FormatMono16
	case 2:
		hwa.format = al.FormatStereo16
	default:
		return fmt.Errorf("snd/al: can't handle input with channels(%v)", in.Channels())
	}
	hwa.in = in
	hwa.out = make([]byte, in.BufferLen()*2)

	s := al.GenSources(1)
	if code := al.Error(); code != 0 {
		return fmt.Errorf("snd/al: generate source failed [err=%v]", code)
	}
	hwa.source = s[0]
	hwa.buf.src = s[0]

	log.Println("snd/al: latency", Latency())

	return nil
}

func Latency() time.Duration {
	return time.Duration(float64(hwa.in.BufferLen()) / hwa.in.SampleRate() * float64(time.Second) * float64(hwa.buf.size))
}

func Start() {
	if hwa.quit != nil {
		panic("snd/al: hwa.quit not nil")
	}
	hwa.quit = make(chan struct{})
	go func() {
		tick := time.Tick(Latency() / 2)
		for {
			select {
			case <-hwa.quit:
				return
			case <-tick:
				Tick()
			}
		}
	}()
}

func Stop() {
	close(hwa.quit)
}

func Tick() {
	start := time.Now()

	if code := al.DeviceError(); code != 0 {
		log.Printf("snd/al: unknown device error [err=%v]\n", code)
	}
	if code := al.Error(); code != 0 {
		log.Printf("snd/al: unknown error [err=%v]\n", code)
	}

	bufs := hwa.buf.Get()

	for _, buf := range bufs {
		hwa.tc++
		hwa.in.Prepare(hwa.tc)
		for i, x := range hwa.in.Samples() {
			// clip
			if x > 1 {
				x = 1
			} else if x < -1 {
				x = -1
			}
			n := int16(math.MaxInt16 * x)
			hwa.out[2*i] = byte(n)
			hwa.out[2*i+1] = byte(n >> 8)
		}

		buf.BufferData(hwa.format, hwa.out, int32(hwa.in.SampleRate()))
		if code := al.Error(); code != 0 {
			log.Printf("snd/al: buffer data failed [err=%v]\n", code)
		}
	}

	hwa.source.QueueBuffers(bufs)
	if code := al.Error(); code != 0 {
		log.Printf("snd/al: queue buffer failed [err=%v]\n", code)
	}

	switch hwa.source.State() {
	case al.Initial:
		al.PlaySources(hwa.source)
	case al.Playing:
	case al.Paused:
	case al.Stopped:
		hwa.underruns++
		al.PlaySources(hwa.source)
	}

	hwa.ticktime += time.Now().Sub(start)
	hwa.tickcount++
}

func BufLen() int {
	return len(hwa.buf.bufs)
}

func Underruns() uint64 {
	return hwa.underruns
}

func TickAverge() time.Duration {
	if hwa.tickcount == 0 {
		return 0
	}
	return hwa.ticktime / time.Duration(hwa.tickcount)
}
