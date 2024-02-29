FROM postgres:{{ .Version }}

LABEL maintainer="{{ .Maintainer }}"

# Set environment variables in a single layer
ENV LANG=de_DE.UTF8 DEBIAN_FRONTEND=noninteractive

# Install locales and clean up in the same RUN command
RUN localedef -i de_DE -c -f UTF-8 -A /usr/share/locale/locale.alias de_DE.UTF-8 \
    {{ if and .InstallTools .Tools }}\
    && apt-get update \
    && apt-get install -y --no-install-recommends {{ .Tools }} \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* \
    {{ end }}

# Run postgres
CMD ["postgres"]
