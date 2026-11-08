export default {
  profitAccounting: {
    unconfirmedCharge: 'Zero charge; billing outcome requires review', sourceIncomplete: 'Source retention does not establish complete billing data for this day.', title: 'Profit accounting', description: 'Revenue, procurement cost and gross profit for balance-billed images',
    schedule: 'USD · Previous day settled at 07:00 Asia/Shanghai · Failed generations are not charged',
    reports: 'Daily reports', prices: 'Procurement prices', addPrice: 'Add price version', recalculate: 'Generate / recalculate',
    startDate: 'Start date', endDate: 'End date', refresh: 'Search', date: 'Billing date',
    revenue: 'Usage revenue', cost: 'Known cost', profit: 'Gross profit', images: 'Billed images', requests: 'Requests',
    status: 'Status', complete: 'Complete', incomplete: 'Missing cost data', pending: 'Pending',
    missing: 'Requests to review', actions: 'Actions', detail: 'View details', revisions: 'Revision history', revision: 'Revision',
    account: 'Account', channel: 'Channel', model: 'Actual upstream model', resolution: 'Billing resolution',
    unitPrice: 'Procurement price (USD / image)', effectiveAt: 'Effective time (Asia/Shanghai)', note: 'Configuration reason',
    scope: 'Price scope', chooseScope: 'Search for an account or channel', save: 'Save new version', cancel: 'Cancel',
    priceHelp: 'Account prices override channel defaults. New prices apply from their effective time. To correct history, enter a past effective time and explicitly recalculate that day.',
    emptyReports: 'No reports yet. The system will catch up automatically, or generate a report for a completed day.',
    emptyPrices: 'Configure procurement prices first. Unmatched requests retain revenue but cannot produce a complete profit figure.',
    reason: 'Recalculation reason', reasonHelp: 'Recalculation creates a revision and preserves earlier reports. Customer charges are not changed.',
    detailHelp: 'Grouped by usage posting date. Revenue is counted once per request; costs use each resolution and its effective price.',
    unknown: 'Not recorded', missingPrice: 'Procurement price missing', unknownResolution: 'Resolution missing',
    unknownCount: 'Billed image count missing', inconsistentCount: 'Image count does not match resolution breakdown',
    fillPrice: 'Configure price', changePrice: 'Adjust price', priceVersion: 'Price version',
    saved: 'Price version saved; explicitly recalculate historical reports', calculated: 'Report generated', failed: 'Operation failed. Please retry.',
    required: 'Complete all required fields and enter a non-negative price', zeroPrice: '0 means free procurement. Do not use 0 for unknown costs.',
    scheduled: 'Scheduled settlement', createdAt: 'Calculated at', historyNote: 'Select a revision to inspect its original results.',
    pendingHelp: 'Known cost includes matched records only. Profit is withheld until all missing data is resolved.',
    immediate: 'Leave empty to apply immediately', noChannel: 'No channel snapshot'
  }
}
