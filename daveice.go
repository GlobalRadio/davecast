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
	"os"
	"fmt"
	"log"
    "net/http"
    "strconv"
	"io"
	"time"
	"net"
	"strings"
	"encoding/hex"
    "encoding/binary"
	"crypto/rand"
)

const DAVECAST_DATA     = 0
const DAVECAST_METADATA = 1
const DAVECAST_ANNOUNCE = 2
const DAVECAST_HEADERS  = 3
const DAVECAST_CACHE    = 254
const DAVECAST_DONE     = 255

const ADTS_AAC_2C_44100_48000  = 0
const ADTS_MP3_2C_44100_128000 = 1
const ADTS_AAC_2C_44100_192000 = 2
const ADTS_AAC_2C_44100_128000 = 3
const ADTS_MP3_1C_44100_48000  = 4
const ADTS_AAC_2C_44100_24000  = 5

type nanosec int64
type davecast struct {
	time nanosec

	mtype int
	replica int
	uuid string
	seq uint64

	data []byte
	mountpoint string
	metadata string
	atype int

    headers string
}

type relay struct {
	channel chan []byte
}

var relays []relay
var seq uint64 = 0

func main () {
	server := os.Args[1]
	stream := os.Args[2]
	
	for n := 3; n < len(os.Args); n++ {
		var r relay
		r.channel = make(chan []byte, 1000)
		relays = append(relays, r)
		if strings.Contains(os.Args[n], "@") {
			go udp_client(strings.Replace(os.Args[n], "@", ":", 1), r.channel)
		} else {
			go tcp_client(os.Args[n], r.channel)
		}
	}

	dc := make(chan davecast, 1000)
	go http_client(server, stream, dc)

	for {
		select {
		case <-time.After(time.Second * 30):
			log.Println(stream, "timeout")
			return
			
		case pdu := <- dc:
			
			pdu.seq = seq
			seq++
			
			for n := 0; n < len(relays); n++ {
				pdu.replica = n
				select {
				case relays[n].channel <- pdu_to_bytes(pdu):
				default:
				}
			}
		}
	}
}

func http_client (server string, stream string, dc chan davecast) {
	uuid, _ := new_uuid()
	
	source := fmt.Sprintf("http://%s/%s", server, stream)

	client := &http.Client{
		//CheckRedirect: redirectPolicyFunc,
	}

	req, err := http.NewRequest("GET", source, nil)
	req.Header.Add("Icy-MetaData", "1")
	resp, err := client.Do(req)

	if err != nil {
		log.Println(stream, "doh", err)
		return
	}
	
	defer resp.Body.Close()

	metaint := 0

	if ice_metadata, ok := resp.Header["Icy-Metaint"]; ok {

		if p, err := strconv.Atoi(ice_metadata[0]); err != nil {
			log.Println(stream, "Icy-Metaint must be an integer")
			return
		} else {
			metaint = p
		}
	}


	mtype := "UNK"
	ice_ainfo := ""

	var pdu davecast
	pdu.mountpoint = stream
	pdu.uuid = string(uuid)
	pdu.atype = ADTS_AAC_2C_44100_48000
	

	if header, ok := resp.Header["Content-Type"]; ok {
		
		switch header[0] {
		case "audio/aac":
			mtype = "AAC"
			
		case "audio/aacp":
			mtype = "AAC"
			
		case "audio/mpeg":
			mtype = "MP3"
		}
	}
	
	if ice_info, ok := resp.Header["Ice-Audio-Info"]; ok {
		info := strings.Split(ice_info[0], ";")

		ice_samplerate := 44100
		ice_bitrate := 48
		ice_channels := 2
		

		for _, v := range info {
			param := strings.Split(v, "=")
			//log.Println(k, v)
			p, _ := strconv.Atoi(param[1])
			
			switch param[0] {
			case "ice-samplerate":
				ice_samplerate = p
				
			case "ice-bitrate":
				ice_bitrate = p
				
			case "ice-channels":
				ice_channels = p
			}
		}

		ice_ainfo = fmt.Sprintf("ADTS_%s_%dC_%d_%d000", mtype, ice_channels, ice_samplerate, ice_bitrate)
	}

	log.Println(ice_ainfo, stream, uuid)

	switch ice_ainfo {
	case "ADTS_AAC_2C_44100_48000":
		pdu.atype = ADTS_AAC_2C_44100_48000
		
	case "ADTS_MP3_2C_44100_128000":
		pdu.atype = ADTS_MP3_2C_44100_128000
		
	case "ADTS_AAC_2C_44100_192000":
		pdu.atype = ADTS_AAC_2C_44100_192000
		
	case "ADTS_AAC_2C_44100_128000":
		pdu.atype = ADTS_AAC_2C_44100_128000

	case "ADTS_MP3_1C_44100_48000":
		pdu.atype = ADTS_MP3_1C_44100_48000

	case "ADTS_AAC_2C_44100_24000":
		pdu.atype = ADTS_AAC_2C_44100_24000
		
	default:
		log.Println("OOPS", ice_ainfo, stream)
		pdu.atype = ADTS_MP3_2C_44100_128000
		//return
	}


	headers := make([]string, 0)
	
	for _, k := range []string {
		"Icy-Genre","Icy-Description","Icy-Name","Icy-Url","Content-Type","Icy-Private","Icy-Pub"} {
		if v, ok := resp.Header[k]; ok {
			headers = append(headers,  fmt.Sprintf("%s\r%s", k, v[0]))
		}
	}
	pdu.headers = strings.Join(headers, "\n")
	
	var adts [65536]byte
	var last byte = 0x00
	offs := 0

	for {
		
		// read metaint bytes
		buff := make([]byte, metaint)
		if nread := readall(resp.Body, buff, metaint); nread != metaint {
			log.Println(stream, "short read", nread, metaint)
			return
		}

		for n := 0; n < len(buff); n++ {
			adts[offs] = buff[n]
			
			if last == 0xff && buff[n] & 0xf0 == 0xf0 {
				
				switch offs {

				case 0: // shouldn't happen ... make it more palatable
					adts[0] = last
					adts[1] = buff[n]
					offs = 2

				case 1:
					
				default:
					size := offs - 1
					var tmp = make([]byte, size)
					copy(tmp[:], adts[0:size])
					pdu.data = tmp
					adts[0] = last
					adts[1] = buff[n]
					offs = 2
					pdu.mtype = DAVECAST_DATA
					dc <- pdu
					last = 0x00
					if( size > 1024) {
						log.Printf("%s frame size %d\n", stream, size)
					}
					
				}
				
			} else {
				last = buff[n]
				offs++
			}
		}
		
	    pdu.mtype = DAVECAST_ANNOUNCE
		dc <- pdu
		
		// read 1 byte
		size := make([]byte, 1)
		if nread := readall(resp.Body, size, 1); nread != 1 {
			log.Println(stream, "short read", nread, metaint)
			return
		}
		// read #bytes from prev step
		msiz := int(size[0]) * 16
		meta := make([]byte, msiz)
		if nread := readall(resp.Body, meta, msiz); nread != msiz {
			log.Println(stream, "short read", nread, msiz)
			return
		}
		//log.Println("meta: ", nread, string(meta[0:msiz]))

        pdu.mtype = DAVECAST_METADATA
        pdu.metadata = string(meta)
		dc <- pdu

        pdu.mtype = DAVECAST_HEADERS
		dc <- pdu
	}
}

func readall(reader io.ReadCloser, p []byte, n int) (int){

	todo := n
	start := 0
	done := 0
	
	for ; todo > 0 ; {
		end := start + todo
		nread, err := reader.Read(p[start:end])
		
		if nread == 0 && err != nil {
			log.Printf("error: %v\n", err)
			break
		}

		done += nread
		
		if nread < todo {
			todo -= nread
			start += nread
		} else {
			break
		}
	}

	return done
}













func udp_client(addr string, messages chan []byte) {

	dst, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
        log.Println("Error resolving:", err.Error())
        os.Exit(1)
	}

	defer func() {
		time.Sleep(3000 * time.Millisecond)
		go udp_client(addr, messages)
    }();
	
	// connect to this socket
	conn, err := net.DialUDP("udp", nil, dst)
	
    if err != nil {
		return
    }
	
	defer func() {
		//log.Printf("udp closing: %s\n", addr);
		conn.Close();
	}();
	
	log.Printf("udp opened: %s\n", addr);
	
	for {
		buff := <- messages
		_, err := conn.Write(buff)
		if err != nil {
			//log.Println("Error writing:", n, err.Error())
			return
		}
	}
}

func tcp_client(addr string, messages chan []byte) {

	defer func() {
		time.Sleep(3000 * time.Millisecond)
		go tcp_client(addr, messages)
    }();
	
	// connect to this socket
	conn, err := net.Dial("tcp", addr)
	
    if err != nil {
		return
    }
	
	defer func() {
		log.Printf("tcp closing: %s\n", addr);
		conn.Close();
	}();
	
	log.Printf("tcp opened: %s\n", addr);
	
	for {
		buff := <- messages
		y := len(buff)

		var size [2]byte
		size[0] = byte(y >> 8)
		size[1] = byte(y % 256)
		
		conn.Write(size[0:2])
		n, err := conn.Write(buff)
		
		if n != y {
			log.Println("Error writing:", n, y)
		}
		
		if err != nil {
			log.Println("Error writing:", n, err.Error())
			return
		}
	}
}

func pdu_to_bytes (pdu davecast) ([]byte) {
	size := 26

	switch pdu.mtype {
	case DAVECAST_DATA:
		size += len(pdu.data)
	case DAVECAST_METADATA:
		size +=len(pdu.metadata)
	case DAVECAST_ANNOUNCE:
		size += (1+len(pdu.mountpoint))
	case DAVECAST_HEADERS:
		size += len(pdu.headers)
	}

	buff := make([]byte, size)
	
	buff[0] = byte(pdu.mtype)
	buff[1] = byte(pdu.replica)

	uuid, _ := hex.DecodeString(pdu.uuid[0:32])
	copy(buff[2:], uuid[:])

	seqn := make([]byte, 8)
	binary.BigEndian.PutUint64(seqn, uint64(pdu.seq))
	copy(buff[18:], seqn[:])

	
	switch pdu.mtype {
	case DAVECAST_DATA:
		copy(buff[26:], pdu.data[:])

	case DAVECAST_METADATA:
		copy(buff[26:], pdu.metadata[:])

	case DAVECAST_ANNOUNCE:
		buff[26] = byte(pdu.atype)
		copy(buff[27:], []byte(pdu.mountpoint))
	
	case DAVECAST_HEADERS:
		copy(buff[26:], pdu.headers[:])
	}

	return buff
}



// RFC 4122
func new_uuid() (string, error) {
	uuid := make([]byte, 16)
	n, err := io.ReadFull(rand.Reader, uuid)
	if n != len(uuid) || err != nil {
		return "", err
	}
	uuid[8] = uuid[8]&^0xc0 | 0x80 // variants 4.1.1
	uuid[6] = uuid[6]&^0xf0 | 0x40 // v4 4.1.3
	return fmt.Sprintf("%x", uuid[:]), nil
}
