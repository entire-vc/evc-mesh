-- +goose Up
CREATE TABLE ext_hello_world_t (id int REFERENCES tasks (id));
