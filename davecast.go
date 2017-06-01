//
//   Copyright 2016, Global Radio Ltd.
//
//   Licensed under the Apache License, Version 2.0 (the "License");
//   you may not use this file except in compliance with the License.
//   You may obtain a copy of the License at
//
//       http://www.apache.org/licenses/LICENSE-2.0
//
//   Unless required by applicable law or agreed to in writing, software
//   distributed under the License is distributed on an "AS IS" BASIS,
//   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//   See the License for the specific language governing permissions and
//   limitations under the License.
//

package main

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"netc" // included
	"ring" // included
)

const use_netc = true

const DAVECAST_DATA = 0
const DAVECAST_METADATA = 1
const DAVECAST_ANNOUNCE = 2
const DAVECAST_HEADERS = 3
const DAVECAST_CONTROL = 255

const ADTS_AAC_2C_44100_48000 = 0
const ADTS_MP3_2C_44100_128000 = 1
const ADTS_AAC_2C_44100_192000 = 2
const ADTS_AAC_2C_44100_128000 = 3
const ADTS_MP3_1C_44100_48000 = 4
const ADTS_AAC_2C_44100_24000 = 5

const DEPTH = 5000 // old
const STREAM_DEPTH = 2000

const DAVECHAN_ACK = 0
const DAVECHAN_NAK = 1
const DAVECHAN_PUB = 2
const DAVECHAN_DEL = 1
const DAVECHAN_SUB = 4
const DAVECHAN_LST = 5

// 6 seconds seems to work well with mplayer's default 320k buffer
// and a 48k stream. icecast can be used to buffer higher bitrates.
// a delay function on output would be useful to offset timeout

const BLIP_TIME = 6  // time after which we consider a stream to have stalled
const FAIL_TIME = 10 // give up if mountpoint cannot be recovered after this
const SYNC_TIME = 15 // stalled stream (missing a frame) will resync after this
const DEAD_TIME = 20 // expire streams completely if not re-synced after this



type nanosec int64
type sec int64
type davecast struct {
	time       nanosec        // timestamp at message receive time
	mtype      int            // message type
	replica    int            // replica number from encoder
	uuid       string         // unique stream id
	seq        uint64         // sequence number
	data       []byte         // ADTS frame data
	mountpoint string         // name of mountpoint in announce message
	metadata   string         // metadata message contents
	atype      int            // audio type
	headers    string         // HTTP headers
	last       sec            // timestamp of last processed message
	upstream   chan *davecast // channel switch message
}

type davechan struct {
	davecast chan *davecast
	reply    chan davechan
	atype    int
	op       int
	key      string
	list     []string
}

// used by stream handler
type stream struct {
	davecast chan *davecast
	davechan chan davechan
	last     sec
}

const LOG_CRIT = 0
const LOG_WARN = 1
const LOG_NOTI = 2
const LOG_INFO = 3
const LOG_DBUG = 4

var log_level int = LOG_NOTI
var req_mounts chan davechan
var req_stream chan davechan

func logit(level int, format string, args ...interface{}) {
	if log_level >= level { log.Printf(format, args...) }
}

func main() {
	if len(os.Args) > 1 {
		if os.Args[1] == "-r" {
			RelayMain()
		} else {
			DavecastMain()
		}
	}
}

func DavecastMain() {

	port := 8000
	
	if d, err := strconv.Atoi(os.Getenv("DEBUG")); err == nil {
		log_level = d
	}

	timer_start()
	log.Printf("Using %d procs\n", runtime.GOMAXPROCS(0))
	time.Sleep(time.Second * 4)

	if len(os.Args) > 1 {
		if p, err := strconv.Atoi(os.Args[1]); err != nil {
			log.Fatal("port must be an integer")
		} else {
			port = p
		}
		log.Println(port, len(os.Args), os.Args)
	}

	req_mounts = make(chan davechan, 1000)
	go MaintainMountpoints(req_mounts)
	
	req_stream = make(chan davechan, 1000)
	go MaintainStreams(req_stream)

	for n := 2; n < len(os.Args); n++ {
		logit(LOG_INFO, "tcp server: %s", os.Args[n])
		channel := make(chan []byte, DEPTH*1000) // ??? what should this be
		go PDURouter(channel)
		go TCPClient(os.Args[n], channel)
	}

	IcecastServer(port)
}

func IcecastServer(port int) {

	// return a list of mountpoints, one per line with leading "/"
	http.HandleFunc("/admin/", func(w http.ResponseWriter, r *http.Request) {
		query := davechan{op: DAVECHAN_LST, reply: make(chan davechan, 10)}
		req_mounts <- query
		reply := <-query.reply
		for _, key := range reply.list {
			fmt.Fprintf(w, "/%s\n", key)
		}
	})

	// serve stream to client
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		r.ProtoMinor = 0 // Icecast likes HTTP/1.0

		mountpoint := r.RequestURI[1:]

		f, ok := w.(http.Flusher)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// subscribe to mountpoint upstream
		stream := make(chan *davecast, STREAM_DEPTH)
		query := davechan{key: mountpoint, op: DAVECHAN_SUB, davecast: stream}
		query.reply = make(chan davechan, 10)
		req_mounts <- query

		if reply := <-query.reply; reply.op != DAVECHAN_ACK { // not present
			logit(LOG_NOTI, "/%s 404\n", mountpoint)
			w.WriteHeader(http.StatusNotFound)
			return
		}

		metaint := 8000 // metadata interval - should be configurable
		metadata := []byte{}

		// read first frame to get codec type, http headers, etc
		pdu, ok := <-stream

		if !ok {
			logit(LOG_NOTI, "/%s 500\n", mountpoint)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		logit(LOG_NOTI, "/%s 200\n", mountpoint)

		p := []string{"audio/aacp", "2", "44100", "48"}

		switch pdu.atype {
		case ADTS_AAC_2C_44100_48000:
			p = []string{"audio/aacp", "2", "44100", "48"}
		case ADTS_MP3_2C_44100_128000:
			p = []string{"audio/mpeg", "2", "44100", "128"}
		case ADTS_AAC_2C_44100_192000:
			p = []string{"audio/aacp", "2", "44100", "192"}
		case ADTS_AAC_2C_44100_128000:
			p = []string{"audio/aacp", "2", "44100", "128"}
		case ADTS_MP3_1C_44100_48000:
			p = []string{"audio/mpeg", "1", "44100", "48"}
		case ADTS_AAC_2C_44100_24000:
			p = []string{"audio/aacp", "2", "44100", "24"}
		}

		w.Header().Set("Content-Type", p[0])
		w.Header().Set("ice-audio-info",
			fmt.Sprintf("ice-samplerate=%s;ice-bitrate=%s;ice-channels=%s",
				p[2], p[3], p[1]))
		w.Header().Set("icy-br", p[3])
		w.Header().Set("icy-private", "0")
		w.Header().Set("icy-pub", "0")
		w.Header().Set("icy-metaint", fmt.Sprintf("%d", metaint))

		for _, v := range strings.Split(pdu.headers, "\n") {
			if strings.ContainsAny(v, "\r") {
				h := strings.Split(v, "\r")
				if len(h[0]) > 0 {
					w.Header().Set(h[0], h[1])
				}
			}
		}

		w.Header().Set("Cache-Control", "no-cache, no-store")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Accept-Ranges", "none")
		w.Header().Set("Connection", "close")
		w.Header().Set("Server", "Icecast 2.3.3-kh11")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers",
			"Origin, Accept, X-Requested-With, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS, HEAD")
		w.Header().Set("Expires", "Mon, 26 Jul 1997 05:00:00 GMT")
		w.Header().Del("Transfer-Encoding")
		w.WriteHeader(http.StatusOK)
		
		sent := 0
		total := 0

		// keep sending frames until they stop or client disconnects
		for {
			select {
			case <-time.After(time.Second * DEAD_TIME):
				return

			case m, more := <-stream:

				if !more {
					return
				}

				switch m.mtype {

				case DAVECAST_METADATA:
					metadata = []byte(m.metadata)

				case DAVECAST_DATA:
					todo := len(m.data)
					start := 0

					for todo > 0 {
						chunk := todo

						if sent+todo > metaint {
							chunk = metaint - sent
						}
						end := start + chunk

						_, e := w.Write(m.data[start:end])
						if e != nil { // client disconnect
							logit(LOG_DBUG, "Client disconnected %v\n", e)
							return
						}
						f.Flush()

						todo -= chunk
						start += chunk
						sent += chunk
						sent = sent % metaint
						total += sent

						if sent == 0 {
							// time for metadata
							metalen := len(metadata)
							len_div_16 := metalen >> 4
							if metalen%16 > 0 {
								len_div_16 += 1
							}
							b := make([]byte, 1+len_div_16*16)
							b[0] = byte(len_div_16)
							copy(b[1:], metadata[:])

							_, e := w.Write(b[:])
							if e != nil {
								logit(LOG_DBUG, "Client disconnected %v\n", e)
								return
							}
							f.Flush()
						}
					}
				}
			}
		}
	})

	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
}

// connects to upstream relay and receives a flood of frames
func TCPClient(addr string, messages chan []byte) {

	if use_netc {
		runtime.LockOSThread()
	}

	defer func() {
		runtime.UnlockOSThread()
		time.Sleep(10 * time.Second)
		go TCPClient(addr, messages)
	}()

	var conn io.ReadCloser
	var err error

	if use_netc {
		conn, err = netc.Dial("tcp", addr)
	} else {
		conn, err = net.Dial("tcp", addr)
	}

	if err != nil {
		logit(LOG_WARN, "tcp failed: %s\n", addr)
		return
	}

	defer func() {
		logit(LOG_WARN, "tcp closing: %s\n", addr)
		conn.Close()
	}()

	logit(LOG_WARN, "tcp opened: %s\n", addr)

	nr := bufio.NewReader(conn)
	
	var size [2]byte
	for {
		if _, err := io.ReadFull(nr, size[:]); err != nil {
			return
		}
		
		length := int(size[0])*256 + int(size[1])
		buff := make([]byte, length)
		
		if length == 0 {
			continue
		}

		if _, err := io.ReadFull(nr, buff); err != nil {
			return
		}

		select {
		case messages <- buff: // ok
		default: // blocked
		}
	}
}


// de-serialise data into datastructure
func MakePDU(msg []byte) *davecast {
	var pdu davecast

	n := len(msg)

	if n < 26 {
		return nil
	}

	pdu.time = timer_offset()
	pdu.mtype = int(msg[0])
	pdu.replica = int(msg[1])
	pdu.uuid = hex.EncodeToString(msg[2:18])
	pdu.seq = binary.BigEndian.Uint64(msg[18:26])

	switch msg[0] {

	case DAVECAST_DATA:
		if n < 28 {
			return nil
		}
		pdu.data = msg[26:n]

	case DAVECAST_METADATA:
		pdu.metadata = string(msg[26:n])

	case DAVECAST_ANNOUNCE:
		if n < 27 {
			return nil
		}
		pdu.mountpoint = string(msg[27:n])
		pdu.atype = int(msg[26])

	case DAVECAST_HEADERS:
		pdu.headers = string(msg[26:n])
	}

	return &pdu
}


// relay supstream to subscriber and deal with adding and removing them
func HandleClients(atype int, upstream chan *davecast, dc chan davechan) {
	cache := davecast{metadata: "", headers: ""}
	clients := make(map[uint64]chan *davecast)
	var n uint64 = 0

	defer func() {
		for _, v := range clients {
			close(v)
		}
	}()

	for {
		select {
		case m := <-dc:
			clients[n] = m.davecast
			n++

		case pdu, ok := <-upstream:
			if !ok {
				return
			}
			switch pdu.mtype {
			case DAVECAST_ANNOUNCE:
				cache.mountpoint = pdu.mountpoint
			case DAVECAST_METADATA:
				cache.metadata = pdu.metadata
			case DAVECAST_HEADERS:
				cache.headers = pdu.headers
			}

			// ensure clients always have this info available in 1st PDU
			pdu.mountpoint = cache.mountpoint
			pdu.metadata = cache.metadata
			pdu.headers = cache.headers
			pdu.atype = atype

			if pdu.seq != cache.seq+1 && pdu.uuid == cache.uuid {
				// non-contiguous sequence numbers in same stream
				logit(LOG_CRIT, "/ %s @ %s %v != %v\n", pdu.uuid,
					pdu.mountpoint, pdu.seq, cache.seq+1)
			}
			cache.seq = pdu.seq

			for k, v := range clients {
				select {
				case v <- pdu: //ok
				default: // buffer full - kill client
					logit(LOG_CRIT, "| %v lost %v\n", pdu.mountpoint, k)
					delete(clients, k)
					close(v)
				}
			}
		}
	}
}

// rendezvous point for publishing and subscribing to mountpoints
func MaintainMountpoints(dc chan davechan) {

	mountpoints := make(map[string]*stream)

	for {
		req := <-dc

		switch req.op {

		case DAVECHAN_SUB:
			if v, ok := mountpoints[req.key]; ok == true {
				v.davechan <- davechan{davecast: req.davecast, key: req.key}
				req.reply <- davechan{op: DAVECHAN_ACK}
			} else {
				req.reply <- davechan{op: DAVECHAN_NAK}
			}

		case DAVECHAN_PUB:
			logit(LOG_DBUG, "? %s\n", req.key)
			if _, ok := mountpoints[req.key]; ok == false {
				logit(LOG_WARN, "+ %s\n", req.key)
				var d stream
				d.davecast = make(chan *davecast, STREAM_DEPTH)
				d.davechan = make(chan davechan, 100)
				d.last = now_minus(0)
				mountpoints[req.key] = &d
				downstrm := make(chan *davecast, STREAM_DEPTH)

				go HandleClients(req.atype, downstrm, d.davechan)

				go func() {
					defer func() {
						var dcs davechan
						dcs.key = req.key
						dcs.davecast = d.davecast
						dcs.op = DAVECHAN_DEL
						dc <- dcs
						close(downstrm)
					}()

					HandleMountpoint(req.key, req.atype, d.davecast, downstrm)
				}()
			}
			dc :=  mountpoints[req.key].davecast
			req.reply <- davechan{key: req.key, davecast: dc}

		case DAVECHAN_DEL:
			if m, ok := mountpoints[req.key]; ok == true {
				logit(LOG_INFO, "! %s @ %v\n", req.key, req.davecast)
				if m.davecast == req.davecast {
					logit(LOG_WARN, "- %s @ %v\n", req.key, req.davecast)
					delete(mountpoints, req.key)
				}
			}

		case DAVECHAN_LST:
			list := make([]string, len(mountpoints))
			i := 0
			for k := range mountpoints {
				list[i] = k
				i++
			}
			sort.Strings(list)
			req.reply <- davechan{list: list}
		}
	}
}

// rendezvous point for publishing streams
func MaintainStreams(dc chan davechan) {
	streams := make(map[string]*stream)

	for {
		req := <-dc

		switch req.op {
		case DAVECHAN_PUB:
			logit(LOG_DBUG, "? %s\n", req.key)
			if _, ok := streams[req.key]; ok == false {
				logit(LOG_INFO, "+ %s\n", req.key)
				var s stream
				s.davecast = make(chan *davecast, STREAM_DEPTH)
				s.last = now_minus(0)
				streams[req.key] = &s

				go func() {
					defer func() {
						var dcs davechan
						dcs.key = req.key
						dcs.davecast = s.davecast
						dcs.op = DAVECHAN_DEL
						dc <- dcs
					}()

					HandleStream(req.key, s.davecast)
				}()
			}

			dc := streams[req.key].davecast
			req.reply <- davechan{davecast: dc, key: req.key}
			
		case DAVECHAN_DEL:
			if m, ok := streams[req.key]; ok == true {
				if m.davecast == req.davecast {
					logit(LOG_INFO, "- %s\n", req.key)
					delete(streams, req.key)
				}
			}

		default:
		}
	}
}

// deduplicate and order frames for a single stream uuid
func HandleStream(uuid string, upstream chan *davecast) {

	buffer := make(map[uint64]*davecast)
	var mountpoint string = "nil"
	var downstream chan *davecast = nil

	var seq uint64 = 0

	last := now_minus(0)
	ticker := time.NewTicker(time.Second * 1)

	for {
		select {
		case <-ticker.C:
			if last < now_minus(DEAD_TIME) {
				logit(LOG_INFO, "< %v\n", uuid)
				return
			}

			if seq != 0 && last < now_minus(SYNC_TIME) {
				logit(LOG_INFO, "* %v\n", uuid)
				seq = 0
				last = now_minus(0)
				break
			}

			if seq != 0 && last < now_minus(BLIP_TIME) {
				logit(LOG_INFO, "%% %v < %v\n", uuid, mountpoint)
			}

			for {
				if pdu, ok := buffer[seq]; ok == true {
					delete(buffer, seq)
					seq++

					if pdu.mtype == DAVECAST_ANNOUNCE {
						
						if downstream == nil {
							dcs := davechan{key: pdu.mountpoint,
							atype: pdu.atype, op: DAVECHAN_PUB}
							dcs.reply = make(chan davechan, 100)
							req_mounts <- dcs
							r := <-dcs.reply
							downstream = r.davecast
						}

						mountpoint = pdu.mountpoint
					}

					if downstream != nil {
						pdu.mountpoint = mountpoint
						pdu.upstream = nil

						select {
						case downstream <- pdu: // ok
							last = now_minus(0)
						default: // blocked
							logit(LOG_CRIT, "| %v @ %v\n", uuid, upstream)
							downstream = nil
						}
					}
				} else {
					break
				}
			}

		case pdu := <-upstream:
			if pdu.uuid != uuid {
				logit(LOG_INFO, "! %v != %v\n", pdu.uuid, uuid)
				break
			}

			if seq == 0 {
				logit(LOG_INFO, "= %v\n", uuid)
				seq = pdu.seq
				buffer = make(map[uint64]*davecast)
				last = now_minus(0)
			}

			if pdu.seq >= seq && pdu.seq < (seq+1000) {
				buffer[pdu.seq] = pdu
			}

		}
	}
}

func PDURouter(upstream chan []byte) {

	streams := make(map[string]*stream)
	ticker := time.NewTicker(time.Second * 5)

	for {
		select {
		case <-ticker.C:
			then := now_minus(DEAD_TIME)

			for k, v := range streams {
				if v.last < then {
					delete(streams, k)
				}
			}

		case msg := <-upstream:
			pdu := MakePDU(msg)

			if pdu == nil {
				logit(LOG_DBUG, "nil pdu\n")
				continue
			}

			if pdu.mtype == DAVECAST_ANNOUNCE {
				if _, ok := streams[pdu.uuid]; ok == false {
					dcs := davechan{key: pdu.uuid, atype: pdu.atype,
					op: DAVECHAN_PUB}
					dcs.reply = make(chan davechan, 1000)
					req_stream <- dcs
					r := <-dcs.reply
					s := stream{last: now_minus(0), davecast: r.davecast}
					streams[pdu.uuid] = &s
				}
			}

			if stream, ok := streams[pdu.uuid]; ok == true {
				if pdu.mtype == DAVECAST_ANNOUNCE {
					stream.last = now_minus(0)
				}

				select {
				case stream.davecast <- pdu: // ok
				default: // blocked
					logit(LOG_CRIT, "| %s\n", pdu.uuid)
					delete(streams, pdu.uuid)
				}
			}
		}
	}
}

func Replay(r *ring.Ring, t nanosec, tmp chan *davecast, up chan *davecast) {
	hit := false
	for v, ok := r.Shift(); ok; v, ok = r.Shift() {
		if v.(*davecast).time > t && !hit {
			hit = true
			continue
		}
		if hit {
			tmp <- v.(*davecast)
		}
	}
	tmp <- &davecast{mtype: DAVECAST_CONTROL, upstream: up}
}

// add quality score to incoming pdus - switch streams based on quality?
func HandleMountpoint(mp string, atype int, in chan *davecast, out chan *davecast) {

	state := davecast{time: 0, last: 0, seq: 0, uuid: ""}
	ticker := time.NewTicker(time.Second * 1)
	buffers := make(map[string]*ring.Ring)

	noncontig := false

	for {
		select {
		case <-ticker.C:
			for k, r := range buffers {
				if r.Peek().(*davecast).last+FAIL_TIME < state.last {
					delete(buffers, k)
				}
			}
			if state.last < now_minus(FAIL_TIME) {
				return
			}

			if state.last > now_minus(int64(BLIP_TIME)) {
				break
			}

			logit(LOG_NOTI, "~ %s @ %s\n", state.uuid, mp)
			state.seq = 0

			for k, r := range buffers {
				tmp := make(chan *davecast, STREAM_DEPTH)
				go Replay(r, state.time, tmp, in)
				in = tmp
				delete(buffers, k)
				break
			}

		case pdu, ok := <-in:
			if !ok {
				return
			}

			if pdu.mtype == DAVECAST_CONTROL {
				in = pdu.upstream
				break
			}

			if state.seq == 0 {
				state.uuid = pdu.uuid
				state.seq = pdu.seq
				logit(LOG_INFO, "= %s @ %s\n", state.uuid, mp)
			}

			pdu.last = now_minus(0)
			
			if pdu.uuid != state.uuid {
				if r, ok := buffers[pdu.uuid]; ok == false {
					buffers[pdu.uuid] = ring.New(1000) // create buffer
				} else if d := r.Peek().(*davecast); d != nil {
					if pdu.seq != d.seq+1 {
						logit(LOG_WARN, "^ %v %v %v\n", pdu.seq, d.seq, mp)
						buffers[pdu.uuid] = ring.New(1000) // reinitialise
					}
				}
				
				buffers[pdu.uuid].Push(pdu)
				break
			}

			if pdu.seq != state.seq {
				// shouldn't happen - should be ordered
				if !noncontig {
					logit(LOG_CRIT, "! %v %v %v\n", pdu.seq, state.seq, mp)
					noncontig = true
				}
				break
			}

			noncontig = false

			select {
			case out <- pdu:
			default:
				logit(LOG_WARN, "- %s\n", mp)
				return
			}

			state.time = pdu.time
			state.last = now_minus(0)
			state.seq++
		}
	}
}

var start time.Time

func timer_start() {
	start = time.Now()
}

func timer_offset() nanosec {
	end := time.Now()
	ns := end.Sub(start).Nanoseconds()

	if ns < 1 { // shouldn't happen?
		return 0
	}
	
	return nanosec(ns)
}

func now_minus(minus int64) sec {
	return sec(time.Now().Unix() - minus)
}


//////////////////////////////////////////////////////////////////////
// Stuff from here down is only for relays and should be split out
//////////////////////////////////////////////////////////////////////

func RelayMain() {
	var n uint64 = 0
	channel := make(chan []byte, 10000)
	control := make(chan chan []byte, 100)
	clients := make(map[uint64]chan []byte)
	producer := "8001"
	consumer := "9001"

	if len(os.Args) > 2 {
		consumer = os.Args[2]
	}

	if len(os.Args) > 3 {
		producer = os.Args[3]
	}

	go TCPServer(producer, control) // for TCPClient instances to connect to
	go TCPRecv(consumer, channel) // for sources to push TCP streams to
	go UDPRecv(consumer, channel) // for sources to push UDP streams to

	if len(os.Args) > 4 {
		if strings.Contains(os.Args[4], "@") {
			go McastRecv(strings.Replace(os.Args[4], "@", ":", 1), channel)
		} else {
			go TCPClient(os.Args[4], channel)
		}
	}

	var x uint64 = 0

	for {
		select {
		case c := <-control: // new client
			clients[n] = c
			n++

		case pdu := <-channel: // new pdu to relay
			if len(pdu) > 0 {
				for k, v := range clients {
					x++
					if x%1000 == 0 && float64(len(v)) > float64(cap(v))*0.9 {
						logit(LOG_DBUG, "%d ~> %d\n", len(v), cap(v))
					}

					select {
					case v <- pdu: // ok
					default: // blocked
						logit(LOG_DBUG, "blocked!")
						close(v)
						delete(clients, k)
					}
				}
			}
		}
	}
}

// Relay stuff - accept connections from sources
func TCPRecv(p string, ch chan []byte) {
	l, err := net.Listen("tcp", "0.0.0.0:"+p)
	if err != nil {
		logit(LOG_CRIT, "Error listening:", err.Error())
		os.Exit(1)
	}

	// Close the listener when the application closes
	defer l.Close()

	logit(LOG_INFO, "TCP", "0.0.0.0:"+p)

	for {
		// Listen for an incoming connection
		if conn, err := l.Accept(); err != nil {
			logit(LOG_WARN, "Error accepting: ", err.Error())
		} else {
			go func(conn net.Conn, ch chan []byte) {
				defer conn.Close()
				nr := bufio.NewReader(conn)

				for {
					var size [2]byte
					if _, err := io.ReadFull(nr, size[:]); err != nil {
						return
					}
					
					buff := make([]byte, int(size[0])*256 + int(size[1]))
					
					if _, err := io.ReadFull(nr, buff); err != nil {
						return
					}
					ch <- buff

				}
			}(conn, ch)
		}
	}
}

// Relay stuff 	- redistribute messages to clients
func TCPServer(port string, control chan chan []byte) {

	l, err := net.Listen("tcp", "0.0.0.0:"+port)
	if err != nil {
		logit(LOG_CRIT, "Error listening:", err.Error())
		os.Exit(1)
	}
	defer l.Close()

	for {
		// Listen for an incoming connection
		if conn, err := l.Accept(); err != nil {
			logit(LOG_WARN, "Error accepting: ", err.Error())
		} else {
			go func() {
				defer conn.Close()

				// 100000 ~ 5sec * 230 streams * 2 feeds (~40pps)
				feed := make(chan []byte, 100000)
				control <- feed

				for {
					if o, ok := <-feed; !ok {
						return
					} else {
						l := len(o)
						b := make([]byte, l+2)
						b[0] = byte(l >> 8)
						b[1] = byte(l % 256)
						copy(b[2:], o[:])

						if n, err := conn.Write(b); n != l+2 || err != nil {
							logit(LOG_INFO, "Error writing: %v", err.Error())
							return
						}
					}
				}
			}()
		}
	}
}

// Relay stuff
func McastRecv(p string, ch chan []byte) {
	addr, err := net.ResolveUDPAddr("udp", p)
	if err != nil {
		logit(LOG_CRIT, "Error resolving:", err.Error())
		os.Exit(1)
	}

	// Listen for incoming connections
	l, err := net.ListenMulticastUDP("udp", nil, addr)
	if err != nil {
		logit(LOG_CRIT, "Error listening:", err.Error())
		os.Exit(1)
	}

	l.SetReadBuffer(9000)

	// Close the listener when the application closes
	defer l.Close()

	logit(LOG_INFO, "MDC", p)

	buf := make([]byte, 9000)

	for {
		if n, _, err := l.ReadFromUDP(buf); err != nil {
			logit(LOG_WARN, "Error receiving: ", err.Error())
		} else {
			//fmt.Printf("!")
			pdu := make([]byte, n)
			copy(pdu, buf)
			ch <- pdu
		}
	}
}

func UDPRecv(p string, ch chan []byte) {
	addr, err := net.ResolveUDPAddr("udp", "0.0.0.0:"+p)
	if err != nil {
		logit(LOG_CRIT, "Error resolving:", err.Error())
		os.Exit(1)
	}

	// Listen for incoming connections
	l, err := net.ListenUDP("udp", addr)
	if err != nil {
		logit(LOG_CRIT, "Error listening:", err.Error())
		os.Exit(1)
	}

	// Close the listener when the application closes
	defer l.Close()

	logit(LOG_INFO, "UDP", "0.0.0.0:"+p)

	buf := make([]byte, 9000)

	for {
		if n, _, err := l.ReadFromUDP(buf); err != nil {
			logit(LOG_WARN, "Error receiving: ", err.Error())
		} else {
			pdu := make([]byte, n)
			copy(pdu, buf)
			ch <- pdu
		}
	}
}



