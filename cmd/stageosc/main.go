package main

import (
	"flag"
	"log"
	"strconv"
	"time"

	"github.com/icedream/go-stagelinq"
	"github.com/samsta/go-osc/osc"
)

const (
	appName    = "StagelinQ to OSC Bridge"
	appVersion = "0.0.1"
	timeout    = 5 * time.Second
)

var stateValues = []string{
	stagelinq.EngineDeck1.TrackArtistName(),
	stagelinq.EngineDeck1.TrackSongName(),

	stagelinq.EngineDeck2.TrackArtistName(),
	stagelinq.EngineDeck2.TrackSongName(),

	stagelinq.EngineDeck3.TrackArtistName(),
	stagelinq.EngineDeck3.TrackSongName(),

	stagelinq.EngineDeck4.TrackArtistName(),
	stagelinq.EngineDeck4.TrackSongName(),
}

func main() {
	verbose := flag.Bool("verbose", false, "Enable verbose output")
	flag.Parse()

	listener, err := stagelinq.ListenWithConfiguration(&stagelinq.ListenerConfiguration{
		DiscoveryTimeout: timeout,
		SoftwareName:     appName,
		SoftwareVersion:  appVersion,
		Name:             "StageOSC",
	})
	if err != nil {
		panic(err)
	}
	defer listener.Close()

	oscClient, err := osc.NewClient("127.0.0.1", 57200)
	if err != nil {
		log.Fatalf("Failed to create OSC client: %v", err)
	}

	listener.AnnounceEvery(time.Second)

	deadline := time.After(timeout)
	foundDevices := []*stagelinq.Device{}

	log.Printf("Listening for devices for %s", timeout)

discoveryLoop:
	for {
		select {
		case <-deadline:
			break discoveryLoop
		default:
			device, deviceState, err := listener.Discover(timeout)
			if err != nil {
				log.Printf("WARNING: %s", err.Error())
				continue discoveryLoop
			}
			if device == nil {
				continue
			}
			// ignore device leaving messages since we do a one-off list
			if deviceState != stagelinq.DevicePresent {
				continue discoveryLoop
			}
			// check if we already found this device before
			for _, foundDevice := range foundDevices {
				if foundDevice.IsEqual(device) {
					continue discoveryLoop
				}
			}
			foundDevices = append(foundDevices, device)
			log.Printf("%s %q %q %q", device.IP.String(), device.Name, device.SoftwareName, device.SoftwareVersion)

			// discover provided services
			log.Println("\tattempting to connect to this device…")
			deviceConn, err := device.Connect(listener.Token(), []*stagelinq.Service{})
			if err != nil {
				log.Printf("WARNING: %s", err.Error())
			} else {
				defer deviceConn.Close()
				log.Println("\trequesting device data services…")
				services, err := deviceConn.RequestServices()
				if err != nil {
					log.Printf("WARNING: %s", err.Error())
					continue
				}

				for _, service := range services {
					log.Printf("\toffers %s at port %d", service.Name, service.Port)
					switch service.Name {
					case "StateMap":
						go func() {
							stateMapTCPConn, err := device.Dial(service.Port)
							if err != nil {
								log.Printf("WARNING: %s", err.Error())
								return
							}
							defer stateMapTCPConn.Close()

							stateMapConn, err := stagelinq.NewStateMapConnection(stateMapTCPConn, listener.Token())
							if err != nil {
								log.Printf("WARNING: %s", err.Error())
								return
							}

							for _, stateValue := range stateValues {
								stateMapConn.Subscribe(stateValue)
							}

							for {
								select {
								case state := <-stateMapConn.StateC():
									log.Printf("\t%s = %v", state.Name, state.Value)
									msg := osc.NewMessage(state.Name)
									msg.Append(state.Value["string"])
									err = oscClient.Send(msg)
									if err != nil {
										log.Printf("Failed to send OSC message: %v", err)
									}
								case err := <-stateMapConn.ErrorC():
									log.Printf("WARNING: %s", err.Error())
									return
								}
							}
						}()

					case "BeatInfo":
						go func() {
							log.Println("\t\tconnecting to BeatInfo...")
							beatInfoTCPConn, err := device.Dial(service.Port)
							if err != nil {
								log.Printf("WARNING: %s", err.Error())
								return
							}
							defer beatInfoTCPConn.Close()

							beatInfoConn, err := stagelinq.NewBeatInfoConnection(beatInfoTCPConn, listener.Token())
							if err != nil {
								log.Printf("WARNING: %s", err.Error())
								return
							}

							log.Println("\t\trequesting start BeatInfo stream...")
							beatInfoConn.StartStream()

							last_beat := [4]int{0, 0, 0, 0}

							for {
								select {
								case bi := <-beatInfoConn.BeatInfoC():
									if *verbose {
										log.Printf("\t\t\t%+v", bi)
									}
									for index, deck := range bi.Players {
										// only send beat messages once per beat, or if the beat counter goes backwards
										if deck.TotalBeats > 0 && (int(deck.Beat) >= last_beat[index]+1 || int(deck.Beat) < last_beat[index]) {
											last_beat[index] = int(deck.Beat)
											msg := osc.NewMessage("/Engine/Deck" + strconv.Itoa(index+1) + "/Beat")
											msg.Append(float32(deck.Beat))
											log.Printf("\t%+v", msg)
											err = oscClient.Send(msg)
											if err != nil {
												log.Printf("Failed to send OSC message: %v", err)
											}
										}
									}
								case err := <-beatInfoConn.ErrorC():
									log.Printf("WARNING: %s", err.Error())
									return
								}
							}
						}()

					}
				}

				log.Println("\tend of list of device data services")
			}
		}
	}

	log.Printf("Found devices: %d", len(foundDevices))
	select {}
}
