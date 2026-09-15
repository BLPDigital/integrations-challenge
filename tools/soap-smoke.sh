#!/bin/sh
# Call the one SOAP operation twice and print both answers.
#
# Twice on purpose: the first delivery of a company's rate table answers a
# RETRYABLE fault with HTTP 200, and the retry succeeds. That is the whole lesson
# of this channel and it is cheaper to see than to read about.
set -eu
ERP_PORT=${1:-8082}
ERP="http://127.0.0.1:$ERP_PORT"
COMPANY=${2:-CH10}

creds=$(curl -fsS -H 'X-Admin-Token: dev-erp-admin' "$ERP/erp-admin/v1/credentials")
user=$(printf '%s' "$creds" | sed -n 's/.*"soap_username":"\([^"]*\)".*/\1/p')
pass=$(printf '%s' "$creds" | sed -n 's/.*"soap_password":"\([^"]*\)".*/\1/p')

env=$(cat <<XML
<?xml version="1.0" encoding="utf-8"?>
<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/"
                  xmlns:fin="urn:blp-erp:finref:1.0"
                  xmlns:cmn="urn:blp-erp:common:1.0"
                  xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <soapenv:Header>
    <wsse:Security xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd" soapenv:mustUnderstand="1">
      <wsse:UsernameToken>
        <wsse:Username>$user</wsse:Username>
        <wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordText">$pass</wsse:Password>
      </wsse:UsernameToken>
    </wsse:Security>
    <fin:RequestContext soapenv:mustUnderstand="1">
      <cmn:CorrelationId>soap-smoke</cmn:CorrelationId>
      <cmn:ConsumerSystem>MINI-BLP</cmn:ConsumerSystem>
    </fin:RequestContext>
  </soapenv:Header>
  <soapenv:Body>
    <fin:GetExchangeRateTable>
      <fin:CompanyCode>$COMPANY</fin:CompanyCode>
      <fin:RateType>DAILY</fin:RateType>
      <fin:ValidTo xsi:nil="true"/>
    </fin:GetExchangeRateTable>
  </soapenv:Body>
</soapenv:Envelope>
XML
)

call() {
	printf '%s' "$env" | curl -fsS -X POST \
		-H 'Content-Type: text/xml; charset=utf-8' \
		-H 'SOAPAction: "urn:blp-erp:finref:1.0/GetExchangeRateTable"' \
		--data-binary @- "$ERP/soap/FinancialReferenceDataService"
}

printf '\n== call 1 for %s (expect a RETRYABLE fault, HTTP 200 with a soap:Fault in the body)\n' "$COMPANY"
call | head -c 900; echo
printf '\n== call 2 for %s (expect the rate table)\n' "$COMPANY"
call | head -c 1400; echo
printf '\n(the response declares ISO-8859-1, uses S: and ns2: prefixes, and its rates carry a decimal comma)\n'
