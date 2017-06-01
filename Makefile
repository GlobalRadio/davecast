all: davecast daveice

clean:
	rm -f davecast daveice

davecast: davecast.go src/netc/netc.go
	GOPATH=$$PWD go build davecast.go

daveice: daveice.go
	go build daveice.go
