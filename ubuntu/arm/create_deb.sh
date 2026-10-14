#!/bin/sh

if [ $UID != 0 ] && [ -z "`which fakeroot`" ]; then
    echo Please install fakeroot first, or run this script as root!
    exit 1
fi

if [ -z "`which dpkg`" ]; then
    echo Please install dpkg first!
    exit 1
fi

# The version comes from VERSION at the repository root, as in the pipeline.
VERSION=$(tr -d '[:space:]' < "$(dirname "$0")/../../VERSION")
sed -i "s/^Version:.*/Version: ${VERSION}/" src/DEBIAN/control
fakeroot dpkg-deb --build src luna2-client.deb

