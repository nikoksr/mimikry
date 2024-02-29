#!/bin/bash
set -e

# Connect to default database with default user, then create a new database and user, connect to it and create an
# extension.
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    CREATE DATABASE my_db;
    CREATE USER my_user WITH PASSWORD '1234';
    \c my_db;
    CREATE EXTENSION IF NOT EXISTS tablefunc;
EOSQL
