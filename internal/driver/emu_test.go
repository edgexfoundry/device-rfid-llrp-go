package driver

import (
	"github.com/iomz/go-llrp"
	"log"
	"net"
	"strconv"
	"time"
)

// emuServer starts an emulated sensor at a specific port
// todo: should have a handle to accept multiple connections and close
func emuServer(port int) {
	listen, err := net.Listen("tcp4", ":"+strconv.Itoa(port))

	if err != nil {
		log.Fatalf("Socket listen port %d failed,%s", port, err)
	}

	defer listen.Close()

	log.Printf("Begin listen port: %d", port)

	conn, err := listen.Accept()
	if err != nil {
		log.Fatalln(err)
	}

	// Send back READER_EVENT_NOTIFICATION
	currentTime := uint64(time.Now().UTC().Nanosecond() / 1000)
	conn.Write(llrp.ReaderEventNotification(1, currentTime))

}
