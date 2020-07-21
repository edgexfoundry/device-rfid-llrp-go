package driver

import (
	"github.impcloud.net/RSP-Inventory-Suite/device-llrp-go/internal/llrp"
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

	// header
	hdr := llrp.NewHdrOnlyMsg(llrp.MsgReaderEventNotification)
	bytes, err := hdr.MarshalBinary()
	_, err = conn.Write(bytes)
	if err != nil {
		log.Fatalf("error writing bytes: %s", err.Error())
	}

	// message data
	currentTime := uint64(time.Now().UTC().Nanosecond() / 1000)
	var success llrp.ConnectionAttemptEvent
	success = 0
	renData := llrp.ReaderEventNotificationData{
		UTCTimestamp:           llrp.UTCTimestamp(currentTime),
		ConnectionAttemptEvent: &success,
	}

	bytes, err = renData.MarshalBinary()
	if err != nil {
		log.Fatalf("error marshalling binary: %s", err.Error())
	}

	_, err = conn.Write(bytes)
	if err != nil {
		log.Fatalf("error writing bytes: %s", err.Error())
	}

}
