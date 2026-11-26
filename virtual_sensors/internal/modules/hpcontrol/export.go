package hpcontrol

import (
	"encoding/json"
	"time"
)

// ParseForecast reads a weather.get_forecasts response for entity ent into
// hour start (unix) -> deg C. Exported (blanket_control, 0.7.0) so other
// modules share hp_control's forecast handling instead of copying it.
func ParseForecast(raw json.RawMessage, ent string) map[int64]float64 { return parseForecast(raw, ent) }

// OutdoorAt is the outdoor temperature at t: the hourly forecast linearly
// interpolated (first/last value held outside it), or tout when the
// forecast is empty.
func OutdoorAt(forecast map[int64]float64, tout float64, t time.Time) float64 {
	return Input{Forecast: forecast, Tout: tout}.outdoorAt(t)
}
