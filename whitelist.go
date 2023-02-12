package main

import (
	"os"
	"log"
	"github.com/leighmacdonald/steamid/v2/steamid"
	"github.com/leighmacdonald/steamweb"
	"bufio"
	"strings"
)

var whitelistEnabled bool
type wlSet map[string]steamid.SID64 //the actual Steam64 value probably won't be used, we are using this map as a set, but seems nice to have
var whitelist wlSet
var blacklist map[string]int

func loadWhitelist() wlSet {
	wl := make(wlSet)

	file, err := os.Open("whitelist.txt")
	if err != nil {
		log.Fatal(err)
	}
	
	level := 1
	scanner := bufio.NewScanner(file)
	exclude_mode := false
	blacklist = make(map[string]int)
	lines_read := 0
	for scanner.Scan() {
		lines_read++
		txt := scanner.Text()
		if txt == "LVL2" { //At level 2, all users on a player's friendlist will have all _their_ friends added as well
			level = 2
			exclude_mode = false
		} else if txt == "LVL1" { //At level 1, all users on a player's friendlist will be added
			level = 1
			exclude_mode = false
		} else if txt == "BLACKLIST" { //All users after this command are removed. This should be the FIRST command if being used
			if lines_read > 1 {
				log.Fatal("BLACKLIST must be first command in whitelist.txt if present")
			}
			exclude_mode = true
		} else {
			if exclude_mode {
				blacklist[txt] = 1
				continue
			}
			sid, err := steamid.StringToSID64(txt)
			if err != nil {
				continue
			}
			whitelistAdd(wl, sid, level)
		}
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
			whitelistAdd(wl, friend_sid, at_level - 1)
		}
	}
}

func isWhitelisted(id string) bool {
	_, exists := whitelist[id]
	return exists
}