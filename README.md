[![Build Status][1]][2]

## Overview

This project tries to help developers to simplify CMPP SMS gateways integrations into existing backend.

CMPP (as known as China Mobile Peer to Peer) is a protocol over TCP that's widely used between SMS gateways and service providers. Although the protocol was introduced several years ago, there are still not many mature SDKs available. The outcome of writing your own implementation is very likely to be time consuming and unsatisfactory.

Since HTTP is common language between servers and front end applications, this project tries to add an HTTP proxy layer in front of the complex cmpp client, making it possible to send sms messages and get status report callbacks over HTTP.

## Features

* Easy integration
* Small memory footprint
* Kubernetes ready

## Work with Kubernetes

We open sourced [cmpp-operator][8] to help developers deploy cmppproxy on a Kubernetes cluster. Basically, it extends kubernetes CRD functionality so that each cmppproxy acts as an self contained app managed in a single place.

## Contributing

Feel free to [file an issue][4] if you encounter issues, or create [pull requests][6].


[1]: https://travis-ci.org/sinoreps/cmppproxy.svg?branch=master
[2]: https://travis-ci.org/sinoreps/cmppproxy
[4]: https://github.com/sinoreps/cmppproxy/issues
[6]: https://github.com/sinoreps/cmppproxy/pulls
[8]: https://github.com/sinoreps/cmpp-operator
