-- +goose Up
CREATE TABLE plain_core (id int);
ALTER TABLE tasks ADD COLUMN x int;
