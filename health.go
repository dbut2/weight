package main

import (
	"fmt"
	"net/http"
	"time"

	"cloud.google.com/go/datastore"
	"github.com/gin-gonic/gin"
)

type EnergyType string

const (
	ActiveEnergy  = "active-energy"
	RestingEnergy = "resting-energy"
	DietaryEnergy = "dietary-energy"
)

type Energy struct {
	Type     EnergyType
	Energy   float64
	Datetime time.Time
}

type SyncEnergy string

const (
	syncActiveEnergy  SyncEnergy = "active_energy"
	syncRestingEnergy SyncEnergy = "basal_energy_burned"
	syncDietaryEnergy SyncEnergy = "dietary_energy"
)

type SyncHealth struct {
	Data struct {
		Metrics []struct {
			Name  SyncEnergy `json:"name"`
			Units string     `json:"units"`
			Data  []struct {
				Date string  `json:"date"`
				Qty  float64 `json:"qty"`
			} `json:"data"`
		} `json:"metrics"`
	} `json:"data"`
}

func parseSyncHealth(sh SyncHealth) ([]Energy, error) {
	var e []Energy

	for _, metric := range sh.Data.Metrics {
		if metric.Units != "kJ" {
			return nil, fmt.Errorf("unknown unit: %s", metric.Units)
		}

		var energyType EnergyType
		switch metric.Name {
		case syncActiveEnergy:
			energyType = ActiveEnergy
		case syncRestingEnergy:
			energyType = RestingEnergy
		case syncDietaryEnergy:
			energyType = DietaryEnergy
		default:
			return nil, fmt.Errorf("unknown energy type: %s", metric.Name)
		}

		for _, data := range metric.Data {
			t, err := time.Parse("2006-01-02 15:04:05 -0700", data.Date)
			if err != nil {
				return nil, err
			}

			energy := Energy{
				Type:     energyType,
				Energy:   data.Qty,
				Datetime: t,
			}

			e = append(e, energy)
		}
	}

	return e, nil
}

func healthPostHandler(c *gin.Context) {
	var health SyncHealth

	err := c.BindJSON(&health)
	if err != nil {
		handleError(c, err)
		return
	}

	energies, err := parseSyncHealth(health)
	if err != nil {
		handleError(c, err)
		return
	}

	period := c.GetHeader("period")
	switch period {
	case "intraday":
		var energyKeys []*datastore.Key
		for range energies {
			energyKeys = append(energyKeys, datastore.IncompleteKey("Energy", nil))
		}

		_, err = dsc().PutMulti(c, energyKeys, energies)
		if err != nil {
			handleError(c, err)
			return
		}
	case "daily":
		for _, energy := range energies {
			rangeStart := time.Date(energy.Datetime.Year(), energy.Datetime.Month(), energy.Datetime.Day(), 0, 0, 0, 0, energy.Datetime.Location())
			rangeEnd := time.Date(energy.Datetime.Year(), energy.Datetime.Month(), energy.Datetime.Day(), 23, 59, 59, 999_999_999, energy.Datetime.Location())

			query := datastore.NewQuery("Energy")
			query = query.FilterField("Type", "=", string(energy.Type))
			query = query.FilterField("Datetime", ">=", rangeStart)
			query = query.FilterField("Datetime", "<=", rangeEnd)

			var dailyEnergies []Energy
			keys, err := dsc().GetAll(c, query, &dailyEnergies)
			if err != nil {
				handleError(c, err)
				return
			}

			err = dsc().DeleteMulti(c, keys)
			if err != nil {
				handleError(c, err)
				return
			}

			_, err = dsc().Put(c, datastore.IncompleteKey("Energy", nil), &energy)
			if err != nil {
				handleError(c, err)
				return
			}
		}
	default:
		handleError(c, fmt.Errorf("unknown period: %s", period))
		return
	}

	c.String(http.StatusOK, "ok")
}

func healthGetHandler(c *gin.Context) {
	c.HTML(http.StatusOK, "health.html", gin.H{})
}
