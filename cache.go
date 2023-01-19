package main

import (
	"github.com/dgraph-io/ristretto"
)

func newCache() *ristretto.Cache {
	cache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: 10000,
		MaxCost: 1000, //If i assign every entry a cost of 1, then this means we can hold at most 1000 items.
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