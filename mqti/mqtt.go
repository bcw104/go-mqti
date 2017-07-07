package mqti

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	MQTT "github.com/eclipse/paho.mqtt.golang"
	"github.com/spf13/viper"
	"github.com/yuin/gopher-lua"
	luajson "layeh.com/gopher-json"
)

const mQTTDefaultPort string = "1883"

// MQTTMessage ...
type MQTTMessage struct {
	MQTT.Message
	MappingConfiguration
}

// PayloadAsString ...
func (m MQTTMessage) PayloadAsString() string {
	return string(m.Payload())
}

// PayloadAsJSON ...
func (m MQTTMessage) PayloadAsJSON() (map[string]interface{}, error) {
	var fields map[string]interface{}

	err := json.Unmarshal(m.Payload(), &fields)

	return fields, err
}

func (m MQTTMessage) jSONFilterShouldSkip(j map[string]interface{}, f []map[string]string, invert bool) bool {
	skip := false

	for _, x := range f {
		skip = invert
		for k, v := range x {
			if (j[k] == v) == invert {
				skip = !invert
			}
			if !invert && skip {
				break
			}
		}
		if !invert && skip {
			break
		}
	}

	return skip
}

func luaFileExecMatch(j, f string) bool {
	var r lua.LBool
	var ok bool

	L := lua.NewState()
	luajson.Preload(L)
	defer L.Close()

	if err := L.DoFile(f); err != nil {
		panic(err)
	}

	if err := L.CallByParam(lua.P{
		Fn:      L.GetGlobal("match"),
		NRet:    1,
		Protect: true,
	}, lua.LString(j)); err != nil {
		panic(err)
	}

	if r, ok = L.Get(-1).(lua.LBool); ok {
		if r {
			return true
		}
	}

	return false
}

func (m MQTTMessage) shouldSkip() bool {
	var f string
	var ok bool

	if f, ok = m.luaFile(); ok {
		payload := m.PayloadAsString()
		return !luaFileExecMatch(payload, f)
	}

	return false
}

func (m MQTTMessage) luaFile() (string, bool) {
	if len(m.MQTT.LUAFile) > 0 {
		return m.MQTT.LUAFile, true
	}
	return "", false
}

func mQTTConfig() map[string]interface{} {
	return viper.GetStringMap("mqtt")
}

func mQTTBrokerURI() string {
	return fmt.Sprintf("%s://%s:%s", mQTTProtocol(), mQTTConfig()["host"], mQTTPort())
}

func mQTTPort() string {
	var port string
	if p := mQTTConfig()["port"]; p != nil {
		port = p.(string)
	} else {
		port = mQTTDefaultPort
	}
	return port
}

func mQTTProtocol() string {
	if p := mQTTConfig()["protocol"]; p != nil {
		return p.(string)
	}
	if mQTTTLSDefined() {
		return "ssl"
	}
	return "tcp"
}

func mQTTClientID() string {
	return mQTTConfig()["client_id"].(string)
}

func mQTTUsername() string {
	u := mQTTConfig()["username"]
	if u != nil {
		return u.(string)
	}
	return ""
}

func mQTTPassword() string {
	p := mQTTConfig()["password"]
	if p != nil {
		return p.(string)
	}
	return ""
}

func mQTTTLSDefined() bool {
	return mQTTConfig()["tls_cert"] != nil && mQTTConfig()["tls_private_key"] != nil
}

func mQTTTLSConfig() tls.Config {
	return *NewTLSConfig(mQTTConfig()["tls_cert"].(string), mQTTConfig()["tls_private_key"].(string))
}

func mQTTCleanSession() bool {
	return mQTTConfig()["clean_session"] != nil && (mQTTConfig()["clean_session"].(bool) == true)
}

// MQTTSubscribe ...
func MQTTSubscribe(incoming chan *MQTTMessage) {
	var outgoing chan *MQTTMessage
	outgoing = incoming

	cs := make(chan os.Signal, 1)
	signal.Notify(cs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-cs
		Log.Error("signal received, exiting")
		os.Exit(0)
	}()

	opts := MQTT.NewClientOptions()

	opts.ClientID = mQTTClientID()
	opts.Username = mQTTUsername()
	opts.Password = mQTTPassword()
	opts.CleanSession = mQTTCleanSession()

	if mQTTTLSDefined() {
		opts.TLSConfig = mQTTTLSConfig()
	}

	opts.AddBroker(mQTTBrokerURI())

	opts.OnConnect = func(c MQTT.Client) {
		var err error
		var config *Config

		config, err = GetConfig()
		if err != nil {
			Log.Fatal(err)
		}

		for _, mapping := range config.Mappings {
			m := mapping
			var f MQTT.MessageHandler = func(client MQTT.Client, msg MQTT.Message) {
				mQTTMessage := &MQTTMessage{msg, m}

				if mQTTMessage.shouldSkip() {
					Log.Debugf("No match! %v", mQTTMessage.PayloadAsString())
				} else {
					Log.Debugf("Match! %v", mQTTMessage.PayloadAsString())
					outgoing <- mQTTMessage
				}
			}

			c.Subscribe(mapping.MQTT.Topic, 0, f)
		}
	}

	opts.OnConnectionLost = func(c MQTT.Client, e error) {
		Log.Error(e)
	}

	client := MQTT.NewClient(opts)

	if token := client.Connect(); token.Wait() && token.Error() != nil {
		Log.Panic(token.Error())
	}

	for {
		time.Sleep(1 * time.Second)
	}
}
