CREATE DATABASE IF NOT EXISTS sourcemod;
USE sourcemod;

CREATE TABLE IF NOT EXISTS bans(steam64 VARCHAR(20) PRIMARY KEY NOT NULL, expires BIGINT NOT NULL, level INT, lastOffence BIGINT);

CREATE TABLE IF NOT EXISTS mgemod_stats (rating INT(4) NOT NULL, steamid VARCHAR(32) NOT NULL, name VARCHAR(64) NOT NULL, wins INT(4) NOT NULL, losses INT(4) NOT NULL, lastplayed INT(11) NOT NULL, hitblip INT(2) NOT NULL)
	 DEFAULT CHARACTER SET utf8 COLLATE utf8_unicode_ci ENGINE = InnoDB;
CREATE TABLE IF NOT EXISTS mgemod_duels (winner VARCHAR(32) NOT NULL, loser VARCHAR(32) NOT NULL, winnerscore INT(4) NOT NULL, loserscore INT(4) NOT NULL, winlimit INT(4) NOT NULL, gametime INT(11) NOT NULL, mapname VARCHAR(64) NOT NULL, arenaname VARCHAR(32) NOT NULL)
	 DEFAULT CHARACTER SET utf8 COLLATE utf8_unicode_ci ENGINE = InnoDB;

CREATE USER IF NOT EXISTS 'mgeme'@'%' IDENTIFIED BY 'bigbear';
GRANT ALTER, CREATE, DELETE, INDEX, INSERT, UPDATE, SELECT ON `sourcemod`.* TO 'mgeme';
FLUSH PRIVILEGES;