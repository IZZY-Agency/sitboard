<script lang="ts">
  // One agent's spend on its card's meta line: output tokens, then Claude
  // Code's cost estimate marked "≈", or "no price". A Codex agent's says
  // "tokens only": Codex keeps no price. The hover explains each.
  import type { Spend } from '../contract.gen';
  import { SPOKEN_ESTIMATE, TOKENS_ONLY, hasSpend, moneyParts, spendTitle, tokensText } from '../data/spend';

  let { spend, codex = false }: { spend: Spend; codex?: boolean } = $props();
  const money = $derived(codex ? { main: '', more: '' } : moneyParts(spend));
</script>

{#if hasSpend(spend)}
  <span class="spend" title={spendTitle(spend, codex)}
    ><span class="visually-hidden">{'Used '}</span><span class="nw"
      ><span class="n">{tokensText(spend)}</span>{' · '}{#if money.main}<span class="est">{money.main}</span>{:else}<span class="none">{codex ? TOKENS_ONLY : 'no price'}</span>{/if}</span
    >{#if money.more}{' '}<span class="more">{money.more.trim()}</span>{/if}{#if money.main}<span class="visually-hidden">{` ${SPOKEN_ESTIMATE}`}</span>{/if}</span
  >
{/if}

<style>
  /* "+ unpriced" is the only part that wraps, onto its own line when the card is narrow. */
  .spend {
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }
  .nw,
  .more {
    white-space: nowrap;
  }
  .n {
    color: var(--fg-body);
  }
  .more {
    color: var(--amber);
  }
  .est {
    text-decoration: underline dotted;
    text-underline-offset: 3px;
  }
</style>
