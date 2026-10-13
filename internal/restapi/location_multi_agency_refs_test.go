package restapi

import (
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"maglev.onebusaway.org/internal/clock"
	"maglev.onebusaway.org/internal/models"
	"maglev.onebusaway.org/internal/utils"
)

// The agency that owns the most stops in the search area becomes
// stopAgencyIndex.fallbackAgencyID, so a stop whose agency the index never
// learned is namespaced under it. These two agencies are sized so that
// resolving a stop through the fallback produces a visibly wrong prefix.
const (
	majorityAgency = "ma-major"
	minorityAgency = "ma-minor"
)

// multiAgencyRefFiles is a two-agency feed built so one arrival's trip status
// points at a stop outside the search area.
//
// The majority agency owns three stops around the query centre. The minority
// agency runs one trip from a stop 55 km north into a fourth stop beside the
// centre, so at the query instant the trip's closest stop is the far one: in
// the entry because the status names it, out of both the matched and nearby
// stop sets because it is far outside the search box.
func multiAgencyRefFiles() map[string]string {
	return map[string]string{
		"agency.txt": "agency_id,agency_name,agency_url,agency_timezone\n" +
			majorityAgency + ",Majority,http://example.com,America/Los_Angeles\n" +
			minorityAgency + ",Minority,http://example.com,America/Los_Angeles\n",
		"routes.txt": "route_id,agency_id,route_short_name,route_long_name,route_type\n" +
			"ma-major-route," + majorityAgency + ",MAJ,Majority Route,3\n" +
			"ma-minor-route," + minorityAgency + ",MIN,Minority Route,3\n",
		"calendar.txt": "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\n" +
			"ma-svc,1,1,1,1,1,1,1,20260101,20261231\n",
		"stops.txt": "stop_id,stop_name,stop_lat,stop_lon\n" +
			"ma-major-1,Majority One,40.000000,-122.000000\n" +
			"ma-major-2,Majority Two,40.002000,-122.000000\n" +
			"ma-major-3,Majority Three,40.004000,-122.000000\n" +
			"ma-minor-near,Minority Near,40.001000,-122.000000\n" +
			"ma-minor-far,Minority Far,40.500000,-122.000000\n",
		"trips.txt": "route_id,service_id,trip_id,trip_headsign,direction_id\n" +
			"ma-major-route,ma-svc,ma-major-trip,Majority,0\n" +
			"ma-minor-route,ma-svc,ma-minor-trip,Minority,0\n",
		// The minority trip departs the far stop before the query instant and
		// reaches the near stop after it, which puts the far stop closest.
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
			"ma-major-trip,08:10:00,08:10:00,ma-major-1,1\n" +
			"ma-major-trip,08:15:00,08:15:00,ma-major-2,2\n" +
			"ma-major-trip,08:20:00,08:20:00,ma-major-3,3\n" +
			"ma-minor-trip,08:00:00,08:00:00,ma-minor-far,1\n" +
			"ma-minor-trip,09:00:00,09:00:00,ma-minor-near,2\n",
	}
}

// TestArrivalsAndDeparturesForLocationNamespacesStatusStopsByOwningAgency
// checks that a stop reached only through a trip status keeps the agency the
// status ID used. Resolving it through the search area's fallback agency
// instead yields a references entry whose ID does not match the status ID, so
// the status stop still does not resolve — the single-agency RABA fixture
// cannot distinguish the two, because there the fallback is always right.
func TestArrivalsAndDeparturesForLocationNamespacesStatusStopsByOwningAgency(t *testing.T) {
	losAngeles, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)
	queryTime := time.Date(2026, time.June, 10, 8, 5, 0, 0, losAngeles)

	api := createTestApiWithGTFSFixture(t, clock.NewMockClock(queryTime),
		"multi-agency-refs.zip", multiAgencyRefFiles())

	query := url.Values{
		"key":           {"TEST"},
		"lat":           {"40.000000"},
		"lon":           {"-122.000000"},
		"radius":        {"2000"},
		"minutesBefore": {"5"},
		"minutesAfter":  {"120"},
	}
	_, model := callAPIHandler[ArrivalsAndDeparturesForLocationResponse](t, api,
		"/api/where/arrivals-and-departures-for-location.json?"+query.Encode())

	entry := model.Data.Entry
	require.NotEmpty(t, entry.ArrivalsAndDepartures)

	farStopID := utils.FormCombinedID(minorityAgency, "ma-minor-far")
	minorityArrival := findArrivalAtStop(t, entry.ArrivalsAndDepartures,
		utils.FormCombinedID(minorityAgency, "ma-minor-near"))
	require.NotNil(t, minorityArrival.TripStatus)
	require.Equal(t, farStopID, minorityArrival.TripStatus.ClosestStop,
		"fixture must put the far minority stop closest, or this test proves nothing")
	require.NotContains(t, entry.StopIDs, farStopID,
		"the far stop must be outside the search area, or the fallback is never exercised")

	referencedStops := make(map[string]bool, len(model.Data.References.Stops))
	for _, s := range model.Data.References.Stops {
		referencedStops[s.ID] = true
	}
	assert.True(t, referencedStops[farStopID],
		"tripStatus.closestStop %s must resolve in references.stops; got %v",
		farStopID, referencedStops)
}

// findArrivalAtStop returns the single arrival at stopID.
func findArrivalAtStop(t *testing.T, arrivals []models.ArrivalAndDeparture, stopID string) models.ArrivalAndDeparture {
	t.Helper()
	for _, a := range arrivals {
		if a.StopID == stopID {
			return a
		}
	}
	ids := make([]string, 0, len(arrivals))
	for _, a := range arrivals {
		ids = append(ids, a.StopID)
	}
	require.FailNow(t, fmt.Sprintf("no arrival at %s; arrivals were at %v", stopID, ids))
	return models.ArrivalAndDeparture{}
}
