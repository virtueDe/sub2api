export default {
  dailyTokenRankingReward: {
      title: 'Daily Token Ranking Rewards',
      description: 'Review the previous day\'s Beijing-time ranking and issue rewards to the top three users.',
      date: 'Settlement date',
      timezone: 'Timezone',
      settled: 'Settled',
      pending: 'Pending',
      candidates: 'Reward candidates',
      rewardRule: '1st $3 · 2nd $2 · 3rd $1',
      rank: 'Rank',
      user: 'User',
      tokens: 'Tokens',
      requests: 'Requests',
      reward: 'Reward',
      status: 'Status',
      note: 'Note',
      settle: 'Issue rewards',
      confirmSettle: 'Issue the ${amount} reward to rank {rank}? This rank cannot be settled twice.',
      settleSuccess: 'Reward issued for rank {rank}',
      loadFailed: 'Failed to load settlement data',
      settleFailed: 'Failed to issue ranking rewards',
      empty: 'No eligible ranking users for this date.',
      statuses: { pending: 'Pending', paid: 'Paid', skipped: 'Skipped', empty: 'No eligible users' }
  }
}
