FROM postgres:{{ .Version }}

LABEL maintainer="{{ .Maintainer }}"

# Set environment variables in a single layer
ENV LANG=de_DE.utf8 DEBIAN_FRONTEND=noninteractive

# Install locales and clean up in the same RUN command
RUN localedef -i de_DE -c -f UTF-8 -A /usr/share/locale/locale.alias de_DE.UTF-8 \
    && apt-get update \
    && apt-get install -y --no-install-recommends postgresql-contrib \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*

# Use a custom script to create the extension
# This script will be executed on container initialization
COPY init-db.sh /docker-entrypoint-initdb.d/

# Make sure the script is executable
RUN chmod +x /docker-entrypoint-initdb.d/init-db.sh
