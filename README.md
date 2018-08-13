aws-elb-replay
==============

Replays Elastic Load Balancer logs against a test host.

Logs are replayed at their original rate, offset by a number of days (which
defaults to 14). For example, replaying logs on 14th August at 10:00am
will replay traffic from 1st August at 10:00am.

## Getting started

1. Get the replayer: `go get github.com/ONSdigital/aws-elb-replay`
2. Copy the ELB logs you want to replay from S3: `aws s3 sync s3://path/to/logs logs`
3. Run the replayer: `aws-elb-replay -host "test-host.internal"`

Logs should be in the current working directory, with the pattern `logs/YEAR/MONTH/DAY/filename.log`

### Licence

Copyright ©‎ 2018, Crown Copyright (Office for National Statistics) (https://www.ons.gov.uk)

Released under MIT license, see [LICENSE](LICENSE.md) for details.
