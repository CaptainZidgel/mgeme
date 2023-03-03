package main

import (
	"github.com/leighmacdonald/steamid/v2/steamid"
	"github.com/leighmacdonald/steamweb"
	"log"
	"strings"
)

type wlSet map[string]steamid.SID64 //the actual Steam64 value probably won't be used, we are using this map as a set, but seems nice to have
var whitelist wlSet
var blacklist map[string]int

func loadWhitelist(rules map[string][]string) wlSet {
	wl := make(wlSet)
	blacklist = make(map[string]int)
	for _, val := range rules["Exclude"] {
		blacklist[val] = 1
	}
	for _, val := range rules["L2"] {
		sid, err := steamid.StringToSID64(val)
		if err != nil {
			continue
		}
		whitelistAdd(wl, sid, 2)
	}
	for _, val := range rules["L1"] {
		sid, err := steamid.StringToSID64(val)
		if err != nil {
			continue
		}
		whitelistAdd(wl, sid, 1)
	}

	return wl
}

//Add this user to whitelist, and bubble down to their friends recursively if at_level > 0
func whitelistAdd(wl wlSet, id steamid.SID64, at_level int) {
	if _, ok := blacklist[id.String()]; ok {
		return
	}
	wl[id.String()] = id //Add to set
	if at_level > 0 {
		list, err := steamweb.GetFriendList(id)
		if err != nil {
			if strings.Contains(err.Error(), "401") { //user has private friendslist. the full error is something like "Invalid status code: 401"
				return
			}
			log.Printf("Err getting friendlist for %s: %v\n", id.String(), err)
			return
		}
		for i := range list {
			friend_sid := list[i].Steamid
			whitelistAdd(wl, friend_sid, at_level-1)
		}
	}
}

func isWhitelisted(id string) bool {
	_, exists := whitelist[id]
	return exists
}
