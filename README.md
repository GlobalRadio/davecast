# Davecast

This is an experimental project to produce highly-available audio
streaming infrastructure compatible with Icecast.

The figures in the doc/ directory show typical Icecast and Davecast
setups - direction of connection establishment is indicated by arrows,
but all data flows from encoders to edge servers.

Under Icecast each mountpoint is maintained via a single HTTP
stream. Some level of redundacy can be achieved by publishing each
mountpoint to two "origin" servers and relaying any missing
mountpoints from the other origin server if it is available. Load
balancers can be used to increase redundancy between "edge" servers
and origin servers but failover results in dropped streams for the
listener. Backup mountpoints can be catered for with explicit
configuration of Icecast, but the failover process is quite noticable
to listeners and often results in an interupted session.

Davecast works by breaking each stream down into individual ADTS
frames and transmitting these over multiple pathways using a variety
of protocols (TCP, UDP, Multicast UDP) to redundant relay
servers. Edge nodes connect to redundant relays at each site and
receive a multiplexed stream of frames. The frames are grouped by
mountpoint and deduplicate/re-ordered such that the received frames
produce an unbroken stream so long as the network is able to carry at
least one copy of each frame. If a frame is irretrievably lost then a
backup stream from a second, redundant, encoder may be substituted. A
short (eg. 10 second) buffer is used to switch in the stream at
roughly the same point in the original transmission such that the
listener hears only a slight glitch where there is a fraction of a
second of repeated or missing audio.

Currently the heavy lifting of dealing with the thousands of listeners
on a typical edge server is handled by Icecast using a local Davecast
process as a master relay. This also helps to provide a buffer between
Davecast and the listener when a stream is stalled during failover
detection.

## How to use it

Davecast is written in Go (golang.org), so we need to compile the
code. Type `make` in the main directory to build the `davecast` and
`daveice` binaries.

First run two relay (`-r` flag) nodes in separate terminals. These
will accept incoming streams via TCP/UDP (port 900x) and expose
streams via TCP (800x).

 terminal1> `./davecast -r 9001 8001`

 terminal2> `./davecast -r 9002 8002`

Second, run a davecast node. This connects to the two relay nodes via tcp and
deduplicates/reassembles the streams:

 terminal3> `./davecast 8000 127.0.0.1:8001 127.0.0.1:8002`

As we are unlikely to have physical encoder machines available we can
simulate them by republishing existing Icecast mountpoints into
davecast using the `daveice` binary. Here we publish two copies of a
stream, each feeding both relay nodes:

 terminal4> `./daveice 81.20.48.165:80 Capital 127.0.0.1:9001 127.0.0.1:9002`

 terminal5> `./daveice 81.20.48.165:80 Capital 127.0.0.1:9001 127.0.0.1:9002`

Each copy of the stream will be relayed to davecast and one copy will
be selected as the "live" stream, the other being kept as a backup if
the live stream dies.

Run mplayer (or vlc) to listen to the stream:

 `mplayer http://127.0.0.1:8000/Capital`

 `vlc http://127.0.0.1:8000/Capital`

You can now simulate outages by stopping (Ctrl-C) and restarting the
source and relay nodes (allowing 10 seconds or so to recover
redundancy between each failure) and the stream to the player should
be uninterrupted. You may need to increase the buffer size in the
client (-cache in mplayer) for particularly high bitrate streams to
accomodate for a few seconds of stalled data. This is mitigated by
running Icecast in front of Davecast.

# Performance

Currently Davecast will handle around 250 mountpoints (a mix of 48Kbps
AAC and 128Kbps MP3) each of which has two encoders and two replicas
of each encoding at around 1 to 1.5 VPCUs in an 8 VPCU virtual machine
on a modest server class system (2x 2.6GHz 8core/16thread Xeon
E5-2640) when compiled with Go v1.7.

## Caveats

I am not a developer and not a Go developer doubly so. This is my
first project written in Go and the code has been developed in an
exploratory manner. Good luck!


LICENSE
-------

    Copyright 2016, Global Radio Ltd.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use these files except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
