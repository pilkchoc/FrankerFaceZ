package server

import (
	"encoding/json"
	"fmt"
	"github.com/satori/go.uuid"
	"golang.org/x/net/websocket"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"syscall"
	"testing"
)

func TCountOpenFDs() uint64 {
	ary, _ := ioutil.ReadDir(fmt.Sprintf("/proc/%d/fd", os.Getpid()))
	return uint64(len(ary))
}

const IgnoreReceivedArguments = 1+2i
func TReceiveExpectedMessage(tb testing.TB, conn *websocket.Conn, messageId int, command Command, arguments interface{}) (ClientMessage, bool) {
	var msg ClientMessage
	var fail bool
	err := FFZCodec.Receive(conn, &msg)
	if err != nil {
		tb.Error(err)
		return msg, false
	}
	if msg.MessageID != messageId {
		tb.Error("Message ID was wrong. Expected", messageId, ", got", msg.MessageID, ":", msg)
		fail = true
	}
	if msg.Command != command {
		tb.Error("Command was wrong. Expected", command, ", got", msg.Command, ":", msg)
		fail = true
	}
	if arguments != IgnoreReceivedArguments {
		if msg.Arguments != arguments {
			tb.Error("Arguments are wrong. Expected", arguments, ", got", msg.Arguments, ":", msg)
		}
	}
	return msg, !fail
}

func TSendMessage(tb testing.TB, conn *websocket.Conn, messageId int, command Command, arguments interface{}) bool {
	err := FFZCodec.Send(conn, ClientMessage{MessageID: messageId, Command: command, Arguments: arguments})
	if err != nil {
		tb.Error(err)
	}
	return err == nil
}

func TestSubscriptionAndPublish(t *testing.T) {
	var doneWg sync.WaitGroup
	var readyWg sync.WaitGroup

	const TestChannelName = "testchannel"
	const TestCommand = "testdata"
	const TestData = "123456789"

	GenerateKeys("/tmp/test_naclkeys.json", "2", "+ZMqOmxhaVrCV5c0OMZ09QoSGcJHuqQtJrwzRD+JOjE=")
	DumpCache()
	conf := &Config{
		UseSSL:       false,
		NaclKeysFile: "/tmp/test_naclkeys.json",
		SocketOrigin: "localhost:2002",
	}
	serveMux := http.NewServeMux()
	SetupServerAndHandle(conf, nil, serveMux)

	server := httptest.NewUnstartedServer(serveMux)
	server.Start()

	wsUrl := fmt.Sprintf("ws://%s/", server.Listener.Addr().String())
	originUrl := fmt.Sprintf("http://%s", server.Listener.Addr().String())
	publishUrl := fmt.Sprintf("http://%s/pub_msg", server.Listener.Addr().String())

	conn, err := websocket.Dial(wsUrl, "", originUrl)
	if err != nil {
		t.Error(err)
		return
	}
	doneWg.Add(1)
	readyWg.Add(1)

	go func(conn *websocket.Conn) {
		TSendMessage(t, conn, 1, HelloCommand, []interface{}{"ffz_0.0-test", uuid.NewV4().String()})
		TReceiveExpectedMessage(t, conn, 1, SuccessCommand, IgnoreReceivedArguments)
		TSendMessage(t, conn, 2, "sub", TestChannelName)
		TReceiveExpectedMessage(t, conn, 2, SuccessCommand, nil)

		readyWg.Done()

		TReceiveExpectedMessage(t, conn, -1, TestCommand, TestData)

		conn.Close()
		doneWg.Done()
	}(conn)

	readyWg.Wait()

	form := url.Values{}
	form.Set("cmd", TestCommand)
	argsBytes, _ := json.Marshal(TestData)
	form.Set("args", string(argsBytes))
	form.Set("channel", TestChannelName)
	form.Set("scope", MsgTargetTypeChat.Name())

	sealedForm, err := SealRequest(form)
	if err != nil {
		t.Error(err)
		server.CloseClientConnections()
		panic("halting test")
	}

	resp, err := http.PostForm(publishUrl, sealedForm)
	if err != nil {
		t.Error(err)
		server.CloseClientConnections()
		panic("halting test")
	}

	respBytes, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	respStr := string(respBytes)

	if resp.StatusCode != 200 {
		t.Error("Publish failed: ", resp.StatusCode, respStr)
		server.CloseClientConnections()
		panic("halting test")
	}

	doneWg.Wait()
	server.Close()
}

func BenchmarkThousandUserSubscription(b *testing.B) {
	var doneWg sync.WaitGroup
	var readyWg sync.WaitGroup

	const TestChannelName = "testchannel"
	const TestCommand = "testdata"
	const TestData = "123456789"

	GenerateKeys("/tmp/test_naclkeys.json", "2", "+ZMqOmxhaVrCV5c0OMZ09QoSGcJHuqQtJrwzRD+JOjE=")
	DumpCache()
	conf := &Config{
		UseSSL:       false,
		NaclKeysFile: "/tmp/test_naclkeys.json",
		SocketOrigin: "localhost:2002",
	}
	serveMux := http.NewServeMux()
	SetupServerAndHandle(conf, nil, serveMux)

	server := httptest.NewUnstartedServer(serveMux)
	server.Start()

	wsUrl := fmt.Sprintf("ws://%s/", server.Listener.Addr().String())
	originUrl := fmt.Sprintf("http://%s", server.Listener.Addr().String())

	message := ClientMessage{MessageID: -1, Command: "testdata", Arguments: TestData}

	fmt.Println()
	fmt.Println(b.N)

	var limit syscall.Rlimit
	syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit)

	limit.Cur = TCountOpenFDs() + uint64(b.N)*2 + 100

	if limit.Cur > limit.Max {
		b.Skip("Open file limit too low")
		return
	}

	syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		conn, err := websocket.Dial(wsUrl, "", originUrl)
		if err != nil {
			b.Error(err)
			break
		}
		doneWg.Add(1)
		readyWg.Add(1)
		go func(i int, conn *websocket.Conn) {
			TSendMessage(b, conn, 1, HelloCommand, []interface{}{"ffz_0.0-test", uuid.NewV4().String()})
			TSendMessage(b, conn, 2, "sub", TestChannelName)

			TReceiveExpectedMessage(b, conn, 1, SuccessCommand, IgnoreReceivedArguments)
			TReceiveExpectedMessage(b, conn, 2, SuccessCommand, nil)

			fmt.Println(i, " ready")
			readyWg.Done()

			TReceiveExpectedMessage(b, conn, -1, TestCommand, TestData)

			conn.Close()
			doneWg.Done()
		}(i, conn)
	}

	readyWg.Wait()

	fmt.Println("publishing...")
	if PublishToChat(TestChannelName, message) != b.N {
		b.Error("not enough sent")
		server.CloseClientConnections()
		panic("halting test instead of waiting")
	}
	doneWg.Wait()

	b.StopTimer()
	server.Close()
	unsubscribeAllClients()
	server.CloseClientConnections()
}
