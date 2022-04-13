package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/exp/constraints"

	"github.com/vsekhar/gocal/internal/cache"
	"github.com/vsekhar/gocal/internal/interval"
	"github.com/vsekhar/gocal/internal/itercal"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	directory "google.golang.org/api/admin/directory/v1"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"
	"googlemaps.github.io/maps"
)

var lookAhead = flag.Duration("next", 24*time.Hour, "process events for the next time period specified, e.g. '72h' (default: '24h'")
var buildingId = flag.String("building", "", "building in which to book rooms (e.g. 'tor-111')")
var floor = flag.Int("floor", 0, "preferred floor")
var section = flag.Int("section", 0, "preferred section")
var credentialFile = flag.String("credentials", "credentials.json", "credentials file")
var tokenFile = flag.String("token", "token.json", "token file")
var mapsAPIKeyFile = flag.String("mapsapikey", "mapsapikey.txt", "Google Maps API Key file")
var dryRun = flag.Bool("dryrun", false, "don't actually change anything")
var calendarId = flag.String("calendar", "primary", "calendar ID to operate on")

const roomTag = "#room"
const roomTagDone = "#addedroom"

// Retrieve a token, saves the token, then returns the generated client.
func getClient(config *oauth2.Config) *http.Client {
	// The file token.json stores the user's access and refresh tokens, and is
	// created automatically when the authorization flow completes for the first
	// time.
	tok, err := tokenFromFile(*tokenFile)
	if err != nil {
		tok = getTokenFromWeb(config)
		saveToken(*tokenFile, tok)
	}
	return config.Client(context.Background(), tok)
}

// Request a token from the web, then returns the retrieved token.
func getTokenFromWeb(config *oauth2.Config) *oauth2.Token {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser then type the "+
		"authorization code: \n%v\n", authURL)

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		log.Fatalf("Unable to read authorization code: %v", err)
	}

	tok, err := config.Exchange(context.TODO(), authCode)
	if err != nil {
		log.Fatalf("Unable to retrieve token from web: %v", err)
	}
	return tok
}

// Retrieves a token from a local file.
func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

// Saves a token to a file path.
func saveToken(path string, token *oauth2.Token) {
	log.Printf("Saving credential file to: %s\n", path)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		log.Fatalf("Unable to cache oauth token: %v", err)
	}
	defer f.Close()
	json.NewEncoder(f).Encode(token)
}

func main() {
	ctx := context.Background()
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	go func() {
		<-sigCtx.Done()
		pprof.Lookup("goroutine").WriteTo(os.Stdout, 1)
		panic("interrupt")
	}()

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	flag.Parse()
	if *dryRun {
		log.Printf("Dry run")
	}

	startTime := time.Now()
	endTime := startTime.Add(*lookAhead)
	log.Printf("From %s to %s", startTime, endTime)

	cred, err := ioutil.ReadFile(*credentialFile)
	if err != nil {
		log.Fatalf("Unable to read client secret file: %v", err)
	}

	config, err := google.ConfigFromJSON(cred,
		// If modifying these scopes, delete your previously saved token.json.
		calendar.CalendarReadonlyScope,
		calendar.CalendarEventsScope, // read/write
		directory.AdminDirectoryResourceCalendarReadonlyScope,
	)

	if err != nil {
		log.Fatalf("Unable to parse client secret file to config: %v", err)
	}
	client := getClient(config)

	// Create services
	dirSrv, err := directory.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to retrieve Admin client: %v", err)
	}
	calSrv, err := calendar.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to retrieve Calendar client: %v", err)
	}

	cacheSpace, err := cache.Application("gocal")
	if err != nil {
		log.Fatal(err)
	}

	buildingIndex, err := itercal.Buildings(ctx, cacheSpace, dirSrv)
	if err != nil {
		log.Fatal(err)
	}

	// Lookup the provided building
	b, err := itercal.SearchBuildings(buildingIndex, *buildingId)
	if err != nil {
		log.Fatalf("searching for office '%s': %v", *buildingId, err)
	}
	log.Printf("Inferred building ID: %s\n", b)
	*buildingId = b

	// Get building's timezone
	mapsAPIKey, err := ioutil.ReadFile(*mapsAPIKeyFile)
	if err != nil {
		log.Fatal(err)
	}
	key := strings.TrimSpace(string(mapsAPIKey))
	mapsClient, err := maps.NewClient(maps.WithAPIKey(key))
	if err != nil {
		log.Fatal(err)
	}
	tzr, err := mapsClient.Timezone(ctx, &maps.TimezoneRequest{
		Location: &maps.LatLng{
			// TODO
		},
		Timestamp: time.Now(),
	})
	if err != nil {
		log.Fatal(err)
	}
	_ = tzr

	resourcesInBuildingIndex, err := itercal.ResourcesInBuilding(ctx, cacheSpace, dirSrv, *buildingId)
	if err != nil {
		log.Fatalf("loading resources for building %s: %v", *buildingId, err)
	}

	// TODO: iterate by day, break up chaining of room distance

	freeBusy := make(map[string]calendar.FreeBusyCalendar)
	freeBusyWg := sync.WaitGroup{}
	freeBusyWg.Add(1)
	go func() {
		defer freeBusyWg.Done()
		start := 0
		for start < len(resourcesInBuildingIndex) {
			// tried and failed: 50, 25
			// worked: 10
			const batchSize = 20
			end := start + batchSize
			if end > len(resourcesInBuildingIndex) {
				end = len(resourcesInBuildingIndex)
			}
			req := &calendar.FreeBusyRequest{TimeMin: startTime.Format(time.RFC3339), TimeMax: endTime.Format(time.RFC3339)}
			for i := start; i < end; i++ {
				req.Items = append(req.Items, &calendar.FreeBusyRequestItem{Id: resourcesInBuildingIndex[i].ResourceEmail})
			}
			fc := calSrv.Freebusy.Query(req)
			fr, err := fc.Do()
			if err != nil {
				panic(err)
			}
			for email, cal := range fr.Calendars {
				notFound := false
				if len(cal.Errors) > 0 {
					for _, e := range cal.Errors {
						if e.Reason == "notFound" {
							notFound = true
							continue // just don't add it
						}
						log.Printf("freebusy (%s): %v", email, e)
						os.Exit(1)
					}
				}
				if !notFound {
					freeBusy[email] = cal
				}
			}
			start = end
		}
	}()

	var eventsImGoingTo []*calendar.Event
	err = itercal.ForEachEvent(ctx, calSrv, *calendarId, time.Now(), time.Now().Add(*lookAhead), func(e *calendar.Event) error {
		if e.Start.DateTime == "" {
			// all day event
			return nil
		}
		if e.Status == "cancelled" {
			return nil
		}
		if e.Transparency == "transparent" {
			return nil
		}
		if strings.Contains(e.Summary, roomTag) || strings.Contains(e.Description, roomTag) {
			eventsImGoingTo = append(eventsImGoingTo, e)
			return nil
		}

		// Check for humans >= 2
		humans := 0
		for _, a := range e.Attendees {
			if a.Self && (a.ResponseStatus == "declined" || a.ResponseStatus == "needsAction") {
				return nil
			}
			if !a.Resource && a.ResponseStatus != "declined" {
				humans++
			}
		}
		if humans > 1 {
			eventsImGoingTo = append(eventsImGoingTo, e)
		}
		return nil
	})
	if err != nil {
		log.Fatalf("error: %v", err)
	}

	// Sort resources by email so we can binary search for them when looking up
	// existing room bookings.
	sort.Slice(resourcesInBuildingIndex, func(i, j int) bool {
		return resourcesInBuildingIndex[i].ResourceEmail < resourcesInBuildingIndex[j].ResourceEmail
	})

	roomsImGoingTo := make([]*directory.CalendarResource, len(eventsImGoingTo))
	for eNo, e := range eventsImGoingTo {
		for _, a := range e.Attendees {
			if !a.Resource || a.ResponseStatus != "accepted" {
				continue
			}
			i := sort.Search(len(resourcesInBuildingIndex), func(i int) bool {
				return resourcesInBuildingIndex[i].ResourceEmail >= a.Email
			})
			if i < len(resourcesInBuildingIndex) {
				r := resourcesInBuildingIndex[i]
				if r.ResourceCategory != "CONFERENCE_ROOM" {
					continue
				}
				roomsImGoingTo[eNo] = r
			}
		}
	}

	log.Printf("Going to:\n")
	for i, r := range roomsImGoingTo {
		b := strings.Builder{}
		b.WriteString(fmt.Sprintf("  %d: ", i+1))
		if r != nil {
			b.WriteString(r.GeneratedResourceName)
		} else {
			b.WriteString("(none)")
		}
		b.WriteString(fmt.Sprintf(" (%s)", eventsImGoingTo[i].Summary))
		if eventsImGoingTo[i].AttendeesOmitted {
			b.WriteString("*")
		}
		log.Print(b.String())
	}

	freeBusyWg.Wait()

	for i, r := range roomsImGoingTo {
		event := eventsImGoingTo[i]
		if r != nil {
			continue
		}
		var prevRoom, nextRoom *directory.CalendarResource
		if i > 0 {
			prevRoom = roomsImGoingTo[i-1]
		}
		if i < len(roomsImGoingTo)-1 && roomsImGoingTo[i+1] != nil {
			nextRoom = roomsImGoingTo[i+1]
		}

		// Create a ranked list of all rooms in building based on
		// min(distance(priorRoom), distance(nextRoom))

		idxs := make([]int, len(resourcesInBuildingIndex))
		for j := range idxs {
			idxs[j] = j
		}
		sort.Slice(idxs, func(i, j int) bool {
			if prevRoom == nil && nextRoom == nil {
				if *floor == 0 || *section == 0 {
					log.Printf("must provide -floor and -section (insufficient existing bookings to infer)")
					os.Exit(1)
				}
				prefLoc := &directory.CalendarResource{
					FloorName:    fmt.Sprintf("%d", *floor),
					FloorSection: fmt.Sprintf("%d", *section),
				}
				return distance(prefLoc, resourcesInBuildingIndex[idxs[i]]) <
					distance(prefLoc, resourcesInBuildingIndex[idxs[j]])
			}

			di_prev := distance(prevRoom, resourcesInBuildingIndex[idxs[i]])
			di_next := distance(nextRoom, resourcesInBuildingIndex[idxs[i]])
			dj_prev := distance(prevRoom, resourcesInBuildingIndex[idxs[j]])
			dj_next := distance(nextRoom, resourcesInBuildingIndex[idxs[j]])
			return min(di_prev, di_next) < min(dj_prev, dj_next)
		})

		/*
			log.Printf("room preferences for %s:", event.Summary)
			for _, r := range idxs[:5] {
				log.Printf("  %s", resourcesInBuildingIndex[r].GeneratedResourceName)
			}
		*/

		// book the first one that is free
	rooms:
		for _, idx := range idxs {
			room := resourcesInBuildingIndex[idx]

			fb, ok := freeBusy[room.ResourceEmail]
			if !ok {
				log.Printf("failed to find free/busy calendar for %s", room.ResourceEmail)
				continue rooms
			}
			for _, timePeriod := range fb.Busy {
				e := interval.OrDie(event.Start.DateTime, event.End.DateTime)
				busy := interval.OrDie(timePeriod.Start, timePeriod.End)
				if e.Overlaps(busy) {
					continue rooms
				}
			}

			// Book the room
			roomAttendee := &calendar.EventAttendee{Email: room.ResourceEmail}
			if event.AttendeesOmitted || strings.Contains(event.Summary, roomTag) || strings.Contains(event.Description, roomTag) {
				// Create a new entry
				hold := &calendar.Event{
					Summary:        fmt.Sprintf("Room for '%s'", strings.ReplaceAll(event.Summary, roomTag, roomTagDone)),
					Attachments:    event.Attachments,
					Attendees:      []*calendar.EventAttendee{roomAttendee},
					ColorId:        event.ColorId,
					ConferenceData: event.ConferenceData,
					Description:    strings.ReplaceAll(event.Description, roomTag, roomTagDone),
					HangoutLink:    event.HangoutLink,
					Start:          event.Start,
					End:            event.End,
					Location:       event.Location,
					Transparency:   event.Transparency,
					Visibility:     event.Visibility,
				}
				log.Printf("Creating %s - %s", hold.Summary, room.GeneratedResourceName)
				if !*dryRun {
					if _, err := calSrv.Events.Insert(*calendarId, hold).SendUpdates("none").Do(); err != nil {
						log.Fatal(err)
					}
				}
				if !event.AttendeesOmitted {
					// Remove room tag from original entry
					log.Printf("Removing #room tag from %s", event.Summary)
					patch := &calendar.Event{
						Summary:     strings.ReplaceAll(event.Summary, roomTag, roomTagDone),
						Description: strings.ReplaceAll(event.Description, roomTag, roomTagDone),
					}
					if !*dryRun {
						if _, err = calSrv.Events.Patch(*calendarId, event.Id, patch).SendUpdates("none").Do(); err != nil {
							log.Fatal(err)
						}
					}
				}
			} else {
				// Patch into existing entry
				log.Printf("Adding %s for %s\n", room.GeneratedResourceName, event.Summary)
				patch := new(calendar.Event)
				patch.Attendees = append([]*calendar.EventAttendee(nil), event.Attendees...)
				patch.Attendees = append(patch.Attendees, roomAttendee)
				pc := calSrv.Events.Patch(*calendarId, event.Id, patch).
					SendUpdates("none")
				if !*dryRun {
					_, err := pc.Do()
					if err != nil {
						log.Fatal(err)
					}
				}
			}
			event.Attendees = append(event.Attendees, roomAttendee)
			break
		}

		// TODO:
		//   - Start fetching (cached) free/busy calendars for the whole day for those
		//     rooms in ranked order
		//   - Attempt to add the room to the corresponding Event in eventsImGoingTo,
		//     iterating through rooms until it works
		//   - Add the room to roomsImGoingTo, proceed to next
	}

	// TODO: preferred or disallowed list?

}

func distance(r1, r2 *directory.CalendarResource) int {
	if r1 == nil || r2 == nil {
		return math.MaxInt
	}
	// Distances in approximate meters
	const (
		subsequentChangeOfSection = 5
		firstChangeOfSection      = 5

		subsequentChangeOfFloor = 10
		firstChangeOfFloor      = firstChangeOfSection + subsequentChangeOfFloor
	)

	distance := 0
	f1, f2 := intOrDie(r1.FloorName), intOrDie(r2.FloorName)
	s1, s2 := intOrDie(r1.FloorSection), intOrDie(r2.FloorSection)
	if f1 != f2 {
		distance += firstChangeOfFloor
		distance += (abs(f1-f2) - 1) * subsequentChangeOfFloor
	}
	if s1 != s2 {
		distance += firstChangeOfSection
		distance += (abs(s1-s2) - 1) * subsequentChangeOfSection
	}
	return distance
}

func intOrDie(s string) int {
	if x, err := strconv.ParseInt(s, 10, 64); err != nil {
		log.Fatalf("'%s' cannot be converted to int: %v", s, err)
	} else {
		return int(x)
	}
	panic("unreachable") // suppress compiler error
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func min[T constraints.Ordered](x, y T) T {
	if x < y {
		return x
	}
	return y
}
