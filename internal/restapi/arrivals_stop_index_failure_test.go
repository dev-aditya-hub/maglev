package restapi

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"maglev.onebusaway.org/internal/clock"
	"maglev.onebusaway.org/internal/models"
	"maglev.onebusaway.org/internal/utils"
)

// breakTripStopIndexQuery makes GetStopTimesForTripIDs fail while leaving the
// rest of the arrivals pipeline working, so a request reaches
// buildTripStopIndex and fails there rather than earlier.
//
// Dropping a column does it. sqlc expands this query's SELECT * at generation
// time, so the generated SQL names every stop_times column including timepoint
// and errors with "no such column" once it is gone. The window query that runs
// first selects named columns, none of them timepoint, so it is unaffected —
// which is the whole point: the arrivals have already been matched by the time
// the index is built.
func breakTripStopIndexQuery(t *testing.T, api *RestAPI) {
	t.Helper()
	_, err := api.GtfsManager.GtfsDB.DB.ExecContext(t.Context(),
		"ALTER TABLE stop_times DROP COLUMN timepoint")
	require.NoError(t, err)
}

// A failed stop-index lookup must fail the request. Both fields it feeds would
// otherwise degrade to 0, and for stopSequence 0 is the trip's first stop — a
// client cannot tell that apart from a real position, so a 200 here reports a
// vehicle at the start of its trip whatever the truth was. totalStopsInTrip: 0
// is self-evidently unreal; stopSequence: 0 is not. See #1648.
//
// Before the fix both endpoints answered 200 with stopSequence 0 on every
// arrival.
func TestArrivalsStopIndexFailureIsNotServedAsPositionZero(t *testing.T) {
	stopCodeWithArrivals := "1030"
	wideWindow := url.Values{"minutesBefore": {"1440"}, "minutesAfter": {"1440"}}

	// The two endpoints wrap their arrivals in different entry shapes, so each
	// case brings its own reader for the pre-check below.
	tests := []struct {
		name        string
		endpoint    func(t *testing.T, api *RestAPI) string
		arrivalsLen func(t *testing.T, api *RestAPI, endpoint string) int
	}{
		{
			name: "arrivals-and-departures-for-stop",
			endpoint: func(t *testing.T, api *RestAPI) string {
				agencyID := mustGetAgencies(t, api)[0].ID
				return arrivalsAndDeparturesURL(
					utils.FormCombinedID(agencyID, stopCodeWithArrivals), wideWindow)
			},
			arrivalsLen: func(t *testing.T, api *RestAPI, endpoint string) int {
				_, model := callAPIHandler[ArrivalsAndDeparturesResponse](t, api, endpoint)
				return len(model.Data.Entry.ArrivalsAndDepartures)
			},
		},
		{
			name: "arrivals-and-departures-for-location",
			endpoint: func(t *testing.T, api *RestAPI) string {
				return arrivalsForLocationURL(arrivalsForLocationCenter, wideWindow)
			},
			arrivalsLen: func(t *testing.T, api *RestAPI, endpoint string) int {
				_, model := callAPIHandler[ArrivalsAndDeparturesForLocationResponse](t, api, endpoint)
				return len(model.Data.Entry.ArrivalsAndDepartures)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := createTestApiWithFeed(t, models.GetFixturePath(t, "raba.zip"))
			api.Clock = clock.NewMockClock(arrivalsTestClock)

			endpoint := tt.endpoint(t, api)

			// Confirm the request reaches the stop index at all: without
			// arrivals in the window the handler returns before building it and
			// the test would pass for the wrong reason.
			require.NotZero(t, tt.arrivalsLen(t, api, endpoint),
				"fixture must produce arrivals, or this test proves nothing")

			breakTripStopIndexQuery(t, api)

			resp, model := callAPIHandler[models.ResponseModel](t, api, endpoint)
			assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
			assert.Equal(t, http.StatusInternalServerError, model.Code)
			assert.Equal(t, "internal server error", model.Text)
		})
	}
}

// buildTripStopIndex returns the error itself, rather than an empty index that
// reads as "every trip starts at its first stop".
func TestBuildTripStopIndexReturnsErrorOnQueryFailure(t *testing.T) {
	api := createTestApiWithFeed(t, models.GetFixturePath(t, "raba.zip"))
	breakTripStopIndexQuery(t, api)

	idx, err := api.buildTripStopIndex(t.Context(), []string{"any-trip"})

	require.Error(t, err)
	assert.ErrorContains(t, err, "batch fetch stop times")
	assert.Empty(t, idx.positions, "a failed lookup must not yield a partial index")
	assert.Empty(t, idx.stopCounts)
}

// An empty trip list is not a failure: there is nothing to look up.
func TestBuildTripStopIndexNoTripsIsNotAnError(t *testing.T) {
	api := createTestApiWithFeed(t, models.GetFixturePath(t, "raba.zip"))

	idx, err := api.buildTripStopIndex(t.Context(), nil)

	require.NoError(t, err)
	assert.Empty(t, idx.positions)
}
