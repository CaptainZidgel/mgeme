package main

import (
	"log"
	"database/sql"
	"time"
)

/*
Bans and penalties (penalties are a subset of bans)
A ban prevents a user from que-ing.
A penalty prevents a user from que-ing in reaction to a bait or ragequit. It falls off over shorter periods of time.
A ban that is not a penalty is a reaction to an RGL ban for cheating or harassment or something of the sort. Most likely permanent.
*/

var SelectBan *sql.Stmt
var UpdateBan *sql.Stmt

const dayDur = time.Hour * 24
const weekDur = dayDur * 7

type ban struct {
	steamid string
	expires time.Time
	banLevel int //should be -1 if you're banned for being a cheater and >= 0 for being a baiter
	lastOffence *time.Time
}

//Get a ban from the database and return a pointer to it
func getBan(steamid string) *ban {
	b := &ban{steamid: steamid}
	var expires int64
	var lastOffence *int64
	var level int
	err := SelectBan.QueryRow(steamid).Scan(&expires, level, lastOffence)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		} else {
			log.Fatalf("Error querying bans %v", err)
		}
	}
	b.expires = time.Unix(expires, 0)
	if lastOffence != nil {
		t := time.Unix(*lastOffence, 0)
		b.lastOffence = &t //Can't directly address function returns
	}
	b.banLevel = level
	if !b.isActive() {
		return nil
	}
	return b
}

//This variable can be overwritten in a test to replace time.Now() with whatever time we want
var now = time.Now

//Create a new ban struct with a level of -1 or 0 and apply the penalty if 0.
func createBan(steamid string, leveled bool) *ban {
	b := &ban{steamid: steamid}
	if leveled {
		t := now()
		b.lastOffence = &t
		b.expires = b.lastOffence.Add(expireLadder[0])
	} else {
		b.banLevel = -1
	}
	return b
}

var expireLadder = [7]time.Duration{30 * time.Minute, 1 * time.Hour, 4 * time.Hour, 1 * dayDur, 2 * dayDur, 3 * dayDur, 1 * weekDur}

func (ban *ban) isActive() bool {
	return now().Before(ban.expires)
}

//Take a ban, calculate weeks since last offence, add a new penalty at the appropriate level.
func (ban *ban) newPenalty() {
	if ban.banLevel == -1 {
		return
	}
	
	d := now().Sub(*ban.lastOffence) //the amount of time elapsed since last offence
	weeksSince := int(d.Hours() / 24 / 7) //Convert d to an exact (float) value of weeks, then floor it by casting to int.
	if weeksSince == 0 {	//Penalized within a week of last offence
		ban.banLevel = ban.banLevel + 1	//Go to next tier
	} else {				//Congrats sailor, you went at least a week without offending
		ban.banLevel = clamp(ban.banLevel - weeksSince, 0, 7)	//If you're at tier x and it's been y weeks since last offence, your new offence will be at tier x-y.
	}
	
	t := now()
	ban.lastOffence = &t
	ban.expires = ban.lastOffence.Add(expireLadder[ban.banLevel])
	
	ban.commitBan() //write to database
}

type updBanMock func(steamid string, exp_epoch int64, blvl int, lastoff *time.Time) error
var updateBanMethod updBanMock

func updateBanSql(steamid string, exp_epoch int64, blvl int, lastoff *time.Time) error {
	_, err := UpdateBan.Exec(steamid, exp_epoch, blvl, lastoff)
	return err
}

func updateBanMock(steamid string, exp_epoch int64, blvl int, lastoff *time.Time) error {
	return nil
}

func (ban *ban) commitBan() {
	expiry := ban.expires.Unix()
	err := updateBanMethod(ban.steamid, expiry, ban.banLevel, ban.lastOffence)
	if err != nil {
		log.Fatalf("Error updating ban %v", err)
	}
}

func punishDelinquents(steam64s []string) {
	for _, id := range steam64s {
		b := getBan(id)
		if b == nil {
			b = createBan(id, true)
		} else {
			b.newPenalty()
		}
	}
}