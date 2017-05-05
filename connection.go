package siridb

import (
	"fmt"
	"net"
	"time"

	"github.com/transceptor-technology/go-qpack"
)

// Connection is a SiriDB Connection. Port should be the client port.
type Connection struct {
	host    string
	port    uint16
	pid     uint16
	buf     *Buffer
	respMap map[uint16]chan *Pkg
	OnClose func()
	LogCh   chan string
}

// NewConnection creates a new connection connection
func NewConnection(host string, port uint16) *Connection {
	return &Connection{
		host:    host,
		port:    port,
		pid:     0,
		buf:     NewBuffer(),
		respMap: make(map[uint16]chan *Pkg),
		OnClose: nil,
		LogCh:   nil,
	}
}

// ToString returns a string representing the connection and port.
func (conn *Connection) ToString() string {
	return fmt.Sprintf("%s:%d", conn.host, conn.port)
}

// Info returns siridb info
func (conn *Connection) Info() (interface{}, error) {
	err := conn.connect()

	if err != nil {
		return nil, err
	}

	return conn.Send(CprotoReqInfo, nil, 10)
}

// Manage send a manage server request.
func (conn *Connection) Manage(username, password string, tp int, options map[string]interface{}) (interface{}, error) {
	err := conn.connect()

	if err != nil {
		return nil, err
	}

	return conn.Send(CprotoReqAdmin, []interface{}{username, password, tp, options}, 60)
}

// Connect to a SiriDB connection.
func (conn *Connection) Connect(username, password, dbname string) error {
	err := conn.connect()

	if err != nil {
		return err
	}

	_, err = conn.Send(
		CprotoReqAuth,
		[]string{username, password, dbname},
		10)
	return err
}

// IsConnected returns true when connected.
func (conn *Connection) IsConnected() bool {
	return conn.buf.conn != nil
}

// Query sends a query and returns the result.
func (conn *Connection) Query(query string, timeout uint16) (interface{}, error) {
	return conn.Send(CprotoReqQuery, []interface{}{query, nil}, timeout)
}

// Insert sends data to a SiriDB database.
func (conn *Connection) Insert(data interface{}, timeout uint16) (interface{}, error) {
	return conn.Send(CprotoReqInsert, data, timeout)
}

func getResult(respCh chan *Pkg, timeoutCh chan bool) (interface{}, error) {
	var result interface{}
	var err error

	select {
	case pkg := <-respCh:
		switch pkg.tp {
		case CprotoResQuery, CprotoResInsert, CprotoResInfo, CprotoAckAdminData:
			result, err = qpack.Unpack(pkg.data)
		case CprotoResAuthSuccess, CprotoResAck, CprotoAckAdmin:
			result = true
		case CprotoResFile:
			result = pkg.data
		case CprotoErrMsg, CprotoErrUserAccess, CprotoErrPool, CprotoErrServer, CprotoErrQuery, CprotoErrInsert, CprotoErrAdmin:
			err = NewError(getErrorMsg(pkg.data), pkg.tp)
		case CprotoErrAdminInvalidRequest:
			err = NewError("invalid request", pkg.tp)
		case CprotoErr:
			err = NewError("runtime error", pkg.tp)
		case CprotoErrNotAuthenticated:
			err = NewError("not authenticated", pkg.tp)
		case CprotoErrAuthCredentials:
			err = NewError("invalid credentials", pkg.tp)
		case CprotoErrAuthUnknownDb:
			err = NewError("unknown database", pkg.tp)
		case CprotoErrLoadingDb:
			err = NewError("error loading database", pkg.tp)
		case CprotoErrFile:
			err = NewError("error while downloading file", pkg.tp)
		default:
			err = fmt.Errorf("Unknown package type: %d", pkg.tp)
		}
	case <-timeoutCh:
		err = fmt.Errorf("Query timeout reached")
	}

	return result, err
}

// Send is used to send bytes
func (conn *Connection) Send(tp uint8, data interface{}, timeout uint16) (interface{}, error) {
	pid := conn.pid

	b, err := pack(pid, tp, data)

	if err != nil {
		return nil, err
	}

	respCh := make(chan *Pkg, 1)

	conn.respMap[pid] = respCh

	conn.pid++

	conn.buf.conn.Write(b)

	timeoutCh := make(chan bool, 1)

	go func() {
		time.Sleep(time.Duration(timeout) * time.Second)
		timeoutCh <- true
	}()

	result, err := getResult(respCh, timeoutCh)

	delete(conn.respMap, pid)

	return result, err
}

// Listen to data channels
func (conn *Connection) Listen() {
	for {
		select {
		case pkg := <-conn.buf.DataCh:
			if respCh, ok := conn.respMap[pkg.pid]; ok {
				respCh <- pkg
			} else {
				conn.sendLog("no response channel found for pid %d, probably the task has been cancelled ot timed out.", pkg.pid)
			}
		case err := <-conn.buf.ErrCh:
			conn.sendLog(err.Error())
			conn.buf.conn.Close()
			conn.buf.conn = nil
			if conn.OnClose != nil {
				conn.OnClose()
			}
		}
	}
}

// Close will close an open connection.
func (conn *Connection) Close() {
	if conn.buf.conn != nil {
		conn.sendLog("Closing connection to %s:%d", conn.host, conn.port)
		conn.buf.conn.Close()
	}
}

func (conn *Connection) sendLog(s string, a ...interface{}) {
	msg := fmt.Sprintf(s, a...)
	if conn.LogCh == nil {
		fmt.Println(msg)
	} else {
		conn.LogCh <- msg
	}
}

func (conn *Connection) connect() error {
	if conn.IsConnected() {
		return nil
	}

	cn, err := net.Dial("tcp", conn.ToString())

	if err != nil {
		return err
	}

	conn.sendLog("Connected to %s:%d", conn.host, conn.port)
	conn.buf.conn = cn

	go conn.buf.Read()
	go conn.Listen()

	return nil
}

func getErrorMsg(b []byte) string {
	result, err := qpack.Unpack(b)
	if err != nil {
		return err.Error()
	}
	return result.(map[interface{}]interface{})["error_msg"].(string)
}
