-- Administrator-owned base configuration. Mount a directory containing this
-- file read-only; the Agent never writes back into it.
include '/usr/etc/snort/snort.lua'

-- Set variables after the stock include. Existing ips options are preserved.
-- ips.variables.nets.HOME_NET = '10.0.0.0/8'
alert_json = { file = true }
