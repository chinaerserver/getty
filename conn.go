/******************************************************
# DESC       : tcp/websocket connection
# MAINTAINER : Alex Stocks
# LICENCE    : Apache License 2.0
# EMAIL      : alexstocks@foxmail.com
# MOD        : 2016-08-17 11:21
# FILE       : conn.go
******************************************************/

package getty

import (
	"compress/flate"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"time"
)

import (
	log "github.com/AlexStocks/log4go"
	"github.com/golang/snappy"
	"github.com/gorilla/websocket"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

var (
	launchTime time.Time = time.Now()

// ErrInvalidConnection = errors.New("connection has been closed.")
)

/////////////////////////////////////////
// compress
/////////////////////////////////////////

type CompressType int

const (
	CompressNone            CompressType = flate.NoCompression      // 0
	CompressZip                          = flate.DefaultCompression // -1
	CompressBestSpeed                    = flate.BestSpeed          // 1
	CompressBestCompression              = flate.BestCompression    // 9
	CompressHuffman                      = flate.HuffmanOnly        // -2
	CompressSnappy                       = 10
)

/////////////////////////////////////////
// connection interfacke
/////////////////////////////////////////

type Connection interface {
	ID() uint32
	SetCompressType(CompressType)
	LocalAddr() string
	RemoteAddr() string
	incReadPkgCount()
	incWritePkgCount()
	// update session's active time
	UpdateActive()
	// get session's active time
	GetActive() time.Time
	readDeadline() time.Duration
	// SetReadDeadline sets deadline for the future read calls.
	SetReadDeadline(time.Duration)
	writeDeadline() time.Duration
	// SetWriteDeadlile sets deadline for the future read calls.
	SetWriteDeadline(time.Duration)
	Write(interface{}) (int, error)
	// don't distinguish between tcp connection and websocket connection. Because
	// gorilla/websocket/conn.go:(Conn)Close also invoke net.Conn.Close
	close(int)
}

/////////////////////////////////////////
// getty connection
/////////////////////////////////////////

var (
	connID uint32
)

type gettyConn struct {
	id            uint32
	compress      CompressType
	padding1      uint8
	padding2      uint16
	readCount     uint32        // read count
	writeCount    uint32        // write count
	readPkgCount  uint32        // send pkg count
	writePkgCount uint32        // recv pkg count
	active        int64         // last active, in milliseconds
	rDeadline     time.Duration // network current limiting
	wDeadline     time.Duration
	rLastDeadline time.Time // lastest network read time
	wLastDeadline time.Time // lastest network write time
	local         string    // local address
	peer          string    // peer address
}

func (c *gettyConn) ID() uint32 {
	return c.id
}

func (c *gettyConn) LocalAddr() string {
	return c.local
}

func (c *gettyConn) RemoteAddr() string {
	return c.peer
}

func (c *gettyConn) incReadPkgCount() {
	atomic.AddUint32(&c.readPkgCount, 1)
}

func (c *gettyConn) incWritePkgCount() {
	atomic.AddUint32(&c.writePkgCount, 1)
}

func (c *gettyConn) UpdateActive() {
	atomic.StoreInt64(&(c.active), int64(time.Since(launchTime)))
}

func (c *gettyConn) GetActive() time.Time {
	return launchTime.Add(time.Duration(atomic.LoadInt64(&(c.active))))
}

func (c *gettyConn) Write(interface{}) (int, error) {
	return 0, nil
}

func (c *gettyConn) close(int) {}

func (c gettyConn) readDeadline() time.Duration {
	return c.rDeadline
}

func (c *gettyConn) SetReadDeadline(rDeadline time.Duration) {
	if rDeadline < 1 {
		panic("@rDeadline < 1")
	}

	c.rDeadline = rDeadline
	if c.wDeadline == 0 {
		c.wDeadline = rDeadline
	}
}

func (c gettyConn) writeDeadline() time.Duration {
	return c.wDeadline
}

func (c *gettyConn) SetWriteDeadline(wDeadline time.Duration) {
	if wDeadline < 1 {
		panic("@wDeadline < 1")
	}

	c.wDeadline = wDeadline
	if c.rDeadline == 0 {
		c.rDeadline = wDeadline
	}
}

/////////////////////////////////////////
// getty tcp connection
/////////////////////////////////////////

type gettyTCPConn struct {
	gettyConn
	reader io.Reader
	writer io.Writer
	conn   net.Conn
}

// create gettyTCPConn
func newGettyTCPConn(conn net.Conn) *gettyTCPConn {
	if conn == nil {
		panic("newGettyTCPConn(conn):@conn is nil")
	}
	var localAddr, peerAddr string
	//  check conn.LocalAddr or conn.RemoetAddr is nil to defeat panic on 2016/09/27
	if conn.LocalAddr() != nil {
		localAddr = conn.LocalAddr().String()
	}
	if conn.RemoteAddr() != nil {
		peerAddr = conn.RemoteAddr().String()
	}

	return &gettyTCPConn{
		conn:   conn,
		reader: io.Reader(conn),
		writer: io.Writer(conn),
		gettyConn: gettyConn{
			id:       atomic.AddUint32(&connID, 1),
			local:    localAddr,
			peer:     peerAddr,
			compress: CompressNone,
		},
	}
}

// for zip compress
type writeFlusher struct {
	flusher *flate.Writer
}

func (t *writeFlusher) Write(p []byte) (int, error) {
	var (
		n   int
		err error
	)

	n, err = t.flusher.Write(p)
	if err != nil {
		return n, err
	}
	if err := t.flusher.Flush(); err != nil {
		return 0, err
	}

	return n, nil
}

// set compress type(tcp: zip/snappy, websocket:zip)
func (t *gettyTCPConn) SetCompressType(c CompressType) {
	switch c {
	case CompressNone, CompressZip, CompressBestSpeed, CompressBestCompression, CompressHuffman:
		t.reader = flate.NewReader(t.conn)

		w, err := flate.NewWriter(t.conn, int(c))
		if err != nil {
			panic(fmt.Sprintf("flate.NewReader(flate.DefaultCompress) = err(%s)", err))
		}
		t.writer = &writeFlusher{flusher: w}

	case CompressSnappy:
		t.reader = snappy.NewReader(t.conn)
		// t.writer = snappy.NewWriter(t.conn)
		t.writer = snappy.NewBufferedWriter(t.conn)

	default:
		panic(fmt.Sprintf("illegal comparess type %d", c))
	}
}

// tcp connection read
func (t *gettyTCPConn) read(p []byte) (int, error) {
	var (
		err         error
		currentTime time.Time
		length      int
	)

	if t.rDeadline > 0 {
		// Optimization: update read deadline only if more than 25%
		// of the last read deadline exceeded.
		// See https://github.com/golang/go/issues/15133 for details.
		currentTime = wheel.Now()
		if currentTime.Sub(t.rLastDeadline) > (t.rDeadline >> 2) {
			if err = t.conn.SetReadDeadline(currentTime.Add(t.rDeadline)); err != nil {
				return 0, err
			}
			t.rLastDeadline = currentTime
		}
	}

	length, err = t.reader.Read(p)
	atomic.AddUint32(&t.readCount, uint32(length))
	return length, err
}

// tcp connection write
func (t *gettyTCPConn) Write(pkg interface{}) (int, error) {
	var (
		err         error
		currentTime time.Time
		ok          bool
		p           []byte
	)

	if p, ok = pkg.([]byte); !ok {
		return 0, fmt.Errorf("illegal @pkg{%#v} type", pkg)
	}
	if t.wDeadline > 0 {
		// Optimization: update write deadline only if more than 25%
		// of the last write deadline exceeded.
		// See https://github.com/golang/go/issues/15133 for details.
		currentTime = wheel.Now()
		if currentTime.Sub(t.wLastDeadline) > (t.wDeadline >> 2) {
			if err = t.conn.SetWriteDeadline(currentTime.Add(t.wDeadline)); err != nil {
				return 0, err
			}
			t.wLastDeadline = currentTime
		}
	}

	atomic.AddUint32(&t.writeCount, (uint32)(len(p)))
	return t.writer.Write(p)
}

// close tcp connection
func (t *gettyTCPConn) close(waitSec int) {
	// if tcpConn, ok := t.conn.(*net.TCPConn); ok {
	// tcpConn.SetLinger(0)
	// }

	if t.conn != nil {
		if writer, ok := t.writer.(*snappy.Writer); ok {
			if err := writer.Close(); err != nil {
				log.Error("snappy.Writer.Close() = error{%v}", err)
			}
		}
		t.conn.(*net.TCPConn).SetLinger(waitSec)
		t.conn.Close()
		t.conn = nil
	}
}

/////////////////////////////////////////
// getty websocket connection
/////////////////////////////////////////

type gettyWSConn struct {
	gettyConn
	conn *websocket.Conn
}

// create websocket connection
func newGettyWSConn(conn *websocket.Conn) *gettyWSConn {
	if conn == nil {
		panic("newGettyWSConn(conn):@conn is nil")
	}
	var localAddr, peerAddr string
	//  check conn.LocalAddr or conn.RemoetAddr is nil to defeat panic on 2016/09/27
	if conn.LocalAddr() != nil {
		localAddr = conn.LocalAddr().String()
	}
	if conn.RemoteAddr() != nil {
		peerAddr = conn.RemoteAddr().String()
	}

	gettyWSConn := &gettyWSConn{
		conn: conn,
		gettyConn: gettyConn{
			id:    atomic.AddUint32(&connID, 1),
			local: localAddr,
			peer:  peerAddr,
		},
	}
	conn.EnableWriteCompression(false)
	conn.SetPingHandler(gettyWSConn.handlePing)
	conn.SetPongHandler(gettyWSConn.handlePong)

	return gettyWSConn
}

// set compress type
func (w *gettyWSConn) SetCompressType(c CompressType) {
	switch c {
	case CompressNone, CompressZip, CompressBestSpeed, CompressBestCompression, CompressHuffman:
		w.conn.EnableWriteCompression(true)
		w.conn.SetCompressionLevel(int(c))

	default:
		panic(fmt.Sprintf("illegal comparess type %d", c))
	}
}

func (w *gettyWSConn) handlePing(message string) error {
	var (
		err         error
		currentTime time.Time
	)
	if w.wDeadline > 0 {
		// Optimization: update write deadline only if more than 25%
		// of the last write deadline exceeded.
		// See https://github.com/golang/go/issues/15133 for details.
		currentTime = wheel.Now()
		if currentTime.Sub(w.wLastDeadline) > (w.wDeadline >> 2) {
			if err = w.conn.SetWriteDeadline(currentTime.Add(w.wDeadline)); err != nil {
				return err
			}
			w.wLastDeadline = currentTime
		}
	}

	err = w.conn.WriteMessage(websocket.PongMessage, []byte(message))
	if err == websocket.ErrCloseSent {
		err = nil
	} else if e, ok := err.(net.Error); ok && e.Temporary() {
		err = nil
	}
	if err == nil {
		w.UpdateActive()
	}

	return err
}

func (w *gettyWSConn) handlePong(string) error {
	w.UpdateActive()
	return nil
}

// websocket connection read
func (w *gettyWSConn) read() ([]byte, error) {
	var (
		err         error
		currentTime time.Time
	)
	if w.rDeadline > 0 {
		// Optimization: update read deadline only if more than 25%
		// of the last read deadline exceeded.
		// See https://github.com/golang/go/issues/15133 for details.
		currentTime = wheel.Now()
		if currentTime.Sub(w.rLastDeadline) > (w.rDeadline >> 2) {
			if err = w.conn.SetReadDeadline(currentTime.Add(w.rDeadline)); err != nil {
				return nil, err
			}
			w.rLastDeadline = currentTime
		}
	}

	// w.conn.SetReadDeadline(time.Now().Add(w.rDeadline))
	_, b, e := w.conn.ReadMessage() // the first return value is message type.
	if e == nil {
		// atomic.AddUint32(&w.readCount, (uint32)(l))
		atomic.AddUint32(&w.readPkgCount, 1)
	} else {
		if websocket.IsUnexpectedCloseError(e, websocket.CloseGoingAway) {
			log.Warn("websocket unexpected close error: %v", e)
		}
	}

	return b, e
}

// websocket connection write
func (w *gettyWSConn) Write(pkg interface{}) (int, error) {
	var (
		err         error
		currentTime time.Time
		ok          bool
		p           []byte
	)

	if p, ok = pkg.([]byte); !ok {
		return 0, fmt.Errorf("illegal @pkg{%#v} type", pkg)
	}
	if w.wDeadline > 0 {
		// Optimization: update write deadline only if more than 25%
		// of the last write deadline exceeded.
		// See https://github.com/golang/go/issues/15133 for details.
		currentTime = wheel.Now()
		if currentTime.Sub(w.wLastDeadline) > (w.wDeadline >> 2) {
			if err = w.conn.SetWriteDeadline(currentTime.Add(w.wDeadline)); err != nil {
				return 0, err
			}
			w.wLastDeadline = currentTime
		}
	}

	// atomic.AddUint32(&w.writeCount, 1)
	atomic.AddUint32(&w.writeCount, (uint32)(len(p)))
	// w.conn.SetWriteDeadline(time.Now().Add(w.wDeadline))
	return len(p), w.conn.WriteMessage(websocket.BinaryMessage, p)
}

func (w *gettyWSConn) writePing() error {
	return w.conn.WriteMessage(websocket.PingMessage, []byte{})
}

// close websocket connection
func (w *gettyWSConn) close(waitSec int) {
	w.conn.WriteMessage(websocket.CloseMessage, []byte("bye-bye!!!"))
	conn := w.conn.UnderlyingConn()
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetLinger(waitSec)
	} else if wsConn, ok := conn.(*tls.Conn); ok {
		wsConn.CloseWrite()
	}
	w.conn.Close()
}

/////////////////////////////////////////
// getty udp connection
/////////////////////////////////////////

type UDPContext struct {
	Pkg      []byte
	PeerAddr *net.UDPAddr
}

type gettyUDPConn struct {
	gettyConn
	peerAddr     *net.UDPAddr // for client
	compressType CompressType
	conn         *net.UDPConn // for server
}

func setUDPSocketOptions(conn *net.UDPConn) error {
	// Try setting the flags for both families and ignore the errors unless they
	// both error.
	err6 := ipv6.NewPacketConn(conn).SetControlMessage(ipv6.FlagDst|ipv6.FlagInterface, true)
	err4 := ipv4.NewPacketConn(conn).SetControlMessage(ipv4.FlagDst|ipv4.FlagInterface, true)
	if err6 != nil && err4 != nil {
		return err4
	}
	return nil
}

// create gettyUDPConn
func newGettyUDPConn(conn *net.UDPConn, peerUDPAddr *net.UDPAddr) *gettyUDPConn {
	if conn == nil {
		panic("newGettyWSConn(conn):@conn is nil")
	}

	var localAddr, peerAddr string
	if conn.LocalAddr() != nil {
		localAddr = conn.LocalAddr().String()
	}

	if conn.RemoteAddr() != nil {
		// connected udp
		peerAddr = conn.RemoteAddr().String()
	} else if peerUDPAddr != nil {
		// unconnected udp
		peerAddr = peerUDPAddr.String()
	}

	return &gettyUDPConn{
		conn:     conn,
		peerAddr: peerUDPAddr,
		gettyConn: gettyConn{
			id:       atomic.AddUint32(&connID, 1),
			local:    localAddr,
			peer:     peerAddr,
			compress: CompressNone,
		},
	}
}

func (u *gettyUDPConn) SetCompressType(c CompressType) {
	switch c {
	case CompressNone, CompressZip, CompressBestSpeed, CompressBestCompression, CompressHuffman, CompressSnappy:
		u.compressType = c

	default:
		panic(fmt.Sprintf("illegal comparess type %d", c))
	}
}

// udp connection read
func (u *gettyUDPConn) read(p []byte) (int, *net.UDPAddr, error) {
	var (
		err         error
		currentTime time.Time
		length      int
		addr        *net.UDPAddr
	)

	if u.rDeadline > 0 {
		// Optimization: update read deadline only if more than 25%
		// of the last read deadline exceeded.
		// See https://github.com/golang/go/issues/15133 for details.
		currentTime = wheel.Now()
		if currentTime.Sub(u.rLastDeadline) > (u.rDeadline >> 2) {
			if err = u.conn.SetReadDeadline(currentTime.Add(u.rDeadline)); err != nil {
				return 0, nil, err
			}
			u.rLastDeadline = currentTime
		}
	}

	if u.peerAddr == nil {
		length, addr, err = u.conn.ReadFromUDP(p)
	} else {
		length, err = u.conn.Read(p)
		addr = u.peerAddr
	}
	if err == nil {
		atomic.AddUint32(&u.readCount, uint32(length))
	}

	return length, addr, err
}

// write udp packet, @ctx should be of type UDPContext
func (u *gettyUDPConn) Write(udpCtx interface{}) (int, error) {
	var (
		err         error
		currentTime time.Time
		length      int
		ok          bool
		ctx         UDPContext
		peerAddr    *net.UDPAddr
	)

	if ctx, ok = udpCtx.(UDPContext); !ok {
		return 0, fmt.Errorf("illegal @udpCtx{%#v} type", udpCtx)
	}

	if u.wDeadline > 0 {
		// Optimization: update write deadline only if more than 25%
		// of the last write deadline exceeded.
		// See https://github.com/golang/go/issues/15133 for details.
		currentTime = wheel.Now()
		if currentTime.Sub(u.wLastDeadline) > (u.wDeadline >> 2) {
			if err = u.conn.SetWriteDeadline(currentTime.Add(u.wDeadline)); err != nil {
				return 0, err
			}
			u.wLastDeadline = currentTime
		}
	}

	atomic.AddUint32(&u.writeCount, (uint32)(len(ctx.Pkg)))
	peerAddr = ctx.PeerAddr
	if u.peerAddr != nil {
		peerAddr = u.peerAddr
	}
	length, _, err = u.conn.WriteMsgUDP(ctx.Pkg, nil, peerAddr)
	return length, err
}

// close udp connection
func (u *gettyUDPConn) close(_ int) {
	if u.conn != nil {
		u.conn.Close()
		u.conn = nil
	}
}
