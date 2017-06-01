package netc

//#include <stdio.h>
//#include <stdlib.h>
//#include <string.h>
//#include <unistd.h>
//#include <sys/types.h>
//#include <sys/socket.h>
//#include <netinet/in.h>
//#include <netdb.h> 
//#include <sys/syscall.h>
//#include <sys/select.h>
//#include <sys/time.h>
//#include <fcntl.h>
//
//int conn(char *hostname, int portno) {
//  struct timeval tv;
//  int sockfd, n, flags;
//  struct sockaddr_in serveraddr;
//  struct hostent *server;
//  server = gethostbyname(hostname);
//  if (server == NULL) return -1;
//  sockfd = socket(AF_INET, SOCK_STREAM, 0);
//  if (sockfd < 0) return -1;
//  bzero((char *) &serveraddr, sizeof(serveraddr));
//  serveraddr.sin_family = AF_INET;
//  bcopy((char *)server->h_addr, (char *)&serveraddr.sin_addr.s_addr, server->h_length);
//  serveraddr.sin_port = htons(portno);
//  if (connect(sockfd, (const struct sockaddr *)&serveraddr, sizeof(serveraddr)) < 0) { close(sockfd); return -1; }
//  tv.tv_sec = 30;
//  tv.tv_usec = 0;
//  setsockopt(sockfd, SOL_SOCKET, SO_RCVTIMEO, (char *)&tv,sizeof(struct timeval));
//  return sockfd;
//}
//
//int go_listen(char *hostname, int portno) {
//  int sockfd, n;
//  struct sockaddr_in serveraddr;
//  struct hostent *server;
//  fprintf(stderr, ">>> gettid %ld\n", syscall(SYS_gettid));
//  server = gethostbyname(hostname);
//  if (server == NULL) return -1;
//  sockfd = socket(AF_INET, SOCK_STREAM, 0);
//  if (sockfd < 0) return -1;
//  bzero((char *) &serveraddr, sizeof(serveraddr));
//  serveraddr.sin_family = AF_INET;
//  bcopy((char *)server->h_addr, (char *)&serveraddr.sin_addr.s_addr, server->h_length);
//  serveraddr.sin_port = htons(portno);
//  if (bind(sockfd, (const struct sockaddr *)&serveraddr, sizeof(serveraddr)) < 0 ) { close(sockfd); return -1; }
//  if (listen(sockfd, 5) < 0) { close(sockfd); return -1; }
//  return sockfd;
//}
//
//int go_read(int fd, void *buff, size_t count) {
//  return read(fd, buff, count);
//}
//#include <time.h>
//uint64_t my_nanotime() {
//  uint64_t t;
//  struct timespec ts;
//  clock_gettime((clockid_t) CLOCK_MONOTONIC_RAW, &ts);
//  t = ts.tv_sec * 1000000000;
//  t += ts.tv_nsec;
//  return t;
//}
// #cgo LDFLAGS: -lrt
import "C"
import "unsafe"
import "strings"
import "strconv"
//import "log"

//import "golang.org/x/sys/unix"
//import "net"

//return read(fd, buff, count);

func Nanotime()(uint64) {
	return uint64(C.my_nanotime())
}

func Connect(addr string)(int) {
    address := strings.Split(addr, ":")
	hostname := C.CString(address[0])
	portno := 80
	if(len(address) > 1) {
		portno, _ = strconv.Atoi(address[1])
	} 
	fd := int(C.conn(hostname, C.int(portno)))
	C.free(unsafe.Pointer(hostname))
	return fd
}
func Listen(network string, addr string) (Netc, error) {
//func Dial(network string, address string) (Netc, error){

    address := strings.Split(addr, ":")
	hostname := C.CString(address[0])
	portno := 80
	if(len(address) > 1) {
		portno, _ = strconv.Atoi(address[1])
	} 
	var t Netc
	t.fd = int(C.go_listen(hostname, C.int(portno)))
	C.free(unsafe.Pointer(hostname))

	if t.fd < 0 {
        var e Netc_err
        e.err = "oops"
        return t, e
    }
	
	return t, nil
}
func (t Netc) Accept()(Netc, error) {
    var x Netc

	x.fd = int(C.accept(C.int(t.fd), nil, nil))

	if x.fd < 0 {
		var e Netc_err
		e.err = "oops"
		return x, e
	}
    return x, nil
}

type Netc struct {
	fd int
}

type Netc_err struct {
	err string
}

func (e Netc_err) Error () (string) {
	return e.err
}

// conn, err := net.Dial("tcp", addr)
func Dial(network string, address string) (Netc, error){
	var t Netc
	var e Netc_err
	e.err = "failed"
	t.fd = Connect(address)
	if t.fd < 0 {
		return t, e
	}
	return t, nil
}

func (t Netc) Close()(error) {
	C.close(C.int(t.fd))
	return nil
}

func (t Netc) Fd() (int) {
	return t.fd
}

func (t Netc) Read(p []byte) (int, error) {
	n := len(p)
	sp := &p[0]

	var e Netc_err

	//nread := int(C.read(C.int(t.fd), unsafe.Pointer(sp), C.size_t(n)))
	nread := int(C.go_read(C.int(t.fd), unsafe.Pointer(sp), C.size_t(n)))

    if nread == -1 {
		e.err = "failed"
		return 0, e
	}

	if nread == 0 {
		e.err = "eof"
		return 0, e
	}

	return nread, nil
}

func (t Netc) Write(p []byte) (int, error) {
	n := len(p)
	sp := &p[0]

	var e Netc_err

	nwrit := int(C.write(C.int(t.fd), unsafe.Pointer(sp), C.size_t(n)))

    if nwrit == -1 {
		e.err = "failed"
		return 0, e
	}

	if nwrit == 0 {
		e.err = "eof"
		return 0, e
	}

	return nwrit, nil
}

/*
func (t Netc) Read(p []byte) (int, error) {
	nread, _, err := unix.Recvfrom(t.fd, p, 0)

    if nread == -1 {
		return 0, err
	}

	if nread == 0 {
		return 0, err
	}

	return nread, nil
}
*/
/*
func Connect(add string)(int) {
    address := strings.Split(add, ":")
	portno := 80

	if(len(address) > 1) {
		portno, _ = strconv.Atoi(address[1])
	} 

	var addr [4]byte
	copy(addr[:], net.ParseIP(address[0]).To4())
	fd, _ := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	unix.Connect(fd, &unix.SockaddrInet4{Port: portno, Addr: addr})
	return fd
}*/


//  //flags = fcntl(sockfd, F_GETFL, 0);                                          
//  //fcntl(sockfd, F_SETFL, flags|O_NONBLOCK);
