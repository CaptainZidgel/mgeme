package main

import (
	"fmt"
	"github.com/captainzidgel/rgl"
	"github.com/dgraph-io/ristretto"
	"github.com/leighmacdonald/steamid/v2/steamid"
	"github.com/leighmacdonald/steamweb"
	"log"
	"time"
)

func newCache() *ristretto.Cache {
	cache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: 10000,
		MaxCost:     1000, //If i assign every entry a cost of 1, then this means we can hold at most 1000 items.
		BufferItems: 64,
	})
	if err != nil {
		panic(err)
	}
	return cache
}

//b := cache.SetWithTTL(key, v, 1, 48 * time.Hour)
//b false -> item not added. b true -> item may have been added

//v, exists := cache.Get(key)

func getRGLSummary(id string) (rgl.Player, error) {
	player, exists := rglCache.Get(id)
	if !exists || player == nil {
		player, err := r.GetPlayer(id)
		if err == nil {
			if player != (rgl.Player{}) { //Found RGL player
				rglCache.SetWithTTL(id, player, 1, 128*time.Hour)
			} else {
				rglCache.SetWithTTL(id, nil, 1, 48*time.Hour)
			}
			return player, nil
		} else {
			return player, err
		}
	} else {
		cast, ok := player.(rgl.Player)
		if !ok {
			log.Println("Something went wrong getting player", player)
		}
		return cast, nil
	}
}

func getSteamSummary(id string) (steamweb.PlayerSummary, error) {
	sid, err := steamid.StringToSID64(id)
	if err != nil {
		return steamweb.PlayerSummary{}, err
	}

	v, exists := steamCache.Get(id)
	if !exists || v == nil {
		sums, err := steamweb.PlayerSummaries([]steamid.SID64{sid})
		if err != nil {
			log.Printf("Warning: Couldn't get summary. %v\n", err)
			return steamweb.PlayerSummary{}, err
		}
		s := sums[0]

		ok := steamCache.SetWithTTL(id, s, 1, 50*time.Hour)
		if !ok {
			log.Println("Warning: Couldn't set summary in cache")
		}
		return s, nil
	} else {
		s := v.(steamweb.PlayerSummary)
		return s, nil
	}
}

func getTotalSummary(id string) steamweb.PlayerSummary {
	player, err := getRGLSummary(id)
	if err != nil {
		log.Printf("Warning: Err getting RGL player summary: %v\n", err)
	}
	summary, err := getSteamSummary(id)
	if err != nil {
		log.Printf("Warning: Err getting Steamcomm summary: %v\n", err)
	}
	if player != (rgl.Player{}) {
		summary.PersonaName = player.Name
	}
	return summary
}

//This is called checkBanCache but is actually a high-level all-case way to get a ban, the only method I should ever call. If the ban isn't cached, it checks all avenues for a ban and caches it.
func checkBanCache(id string) *ban {
	v, exists := banCache.Get("ban" + id)
	if !exists {
		fmt.Printf("Cache miss: querying ban for %s", id) //Use fmt instead of log so each log doesn't begin with Timestamp/force a newline...
		ban := getBan(id)                                 //
		if ban == nil {                                   //
			fmt.Printf(" (Isn't banned)\n")                           //...this lets us print this stuff to the same line
			ok := banCache.SetWithTTL("ban"+id, nil, 1, 24*time.Hour) //Use *ban to dereference pointer. just store the actual ban.
			if !ok {
				log.Println("Warning: Couldn't set cache for nil ban")
			}
			return nil
		} else {
			if ban.isActive() {
				fmt.Printf(" (Is banned)\n")
			} else {
				fmt.Printf(" (Is expired)\n")
			}
			ok := banCache.SetWithTTL("ban"+id, *ban, 1, 24*time.Hour) //ban is a pointer (*ban), so *(*ban) should dereference and cache the struct
			if !ok {
				log.Println("Warning: Couldn't set cache for ban")
			}
			return ban //but return the pointer
		}
	} else {
		fmt.Printf("Cache hit for ban for %s", id)
		if v == nil {
			ban := getBan(id)
			if ban == nil || !ban.isActive() {
				fmt.Printf(" (Isn't banned)\n")
			} else {
				fmt.Printf(" (Is banned)\n")
			}
			return ban
		} else {
			ban := v.(ban)
			if ban.isActive() {
				fmt.Printf(" (Is banned)\n")
			} else {
				fmt.Printf(" (Is expired)\n")
			}
			return &ban
		}
	}
}
