import {createRouter, createWebHashHistory} from 'vue-router'

// Hash routing, deliberately. Inscription URLs like #/asset/<hash> are already
// in circulation, and a hash route needs nothing from the server — the viewer
// keeps working when it is opened from a file, mounted under a path, or served
// by a static host that knows nothing about this app's routes.
export const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    {path: '/', name: 'gallery', component: () => import('@/pages/GalleryPage.vue')},
    {
      path: '/asset/:id',
      name: 'asset',
      component: () => import('@/pages/AssetPage.vue'),
      props: true,
    },
    {
      path: '/collections',
      name: 'collections',
      component: () => import('@/pages/CollectionsPage.vue'),
    },
    {
      path: '/collection/:id',
      name: 'collection',
      component: () => import('@/pages/CollectionPage.vue'),
      props: true,
    },
    // An address is a first-class object here, like an asset or a collection:
    // it has holdings that only the balance table knows, and a link from
    // anywhere an address appears has somewhere to go that is not a filtered
    // gallery. The param is the bech32 address, which needs no encoding.
    {
      path: '/address/:addr',
      name: 'address',
      component: () => import('@/pages/AddressPage.vue'),
      props: true,
    },
    {path: '/dispensers', name: 'market', component: () => import('@/pages/MarketPage.vue')},
    {path: '/chain', name: 'chain', component: () => import('@/pages/ChainPage.vue')},
    // The EVM explorer. Four routes because an EVM has four things worth a
    // page of its own: the chain, an account (which may be a contract), a
    // token's holders, and a single transaction.
    {path: '/evm', name: 'evm', component: () => import('@/pages/evm/EvmOverviewPage.vue')},
    {path: '/evm/blocks', name: 'evm-blocks', component: () => import('@/pages/evm/EvmBlocksPage.vue')},
    {
      path: '/evm/block/:height',
      name: 'evm-block',
      component: () => import('@/pages/evm/EvmBlockPage.vue'),
      props: true,
    },
    {path: '/evm/txs', name: 'evm-txs', component: () => import('@/pages/evm/EvmTxsPage.vue')},
    {path: '/evm/faucet', name: 'evm-faucet', component: () => import('@/pages/evm/EvmFaucetPage.vue')},
    // Three listings over one component, separated only by a filter.
    {
      path: '/evm/contracts',
      name: 'evm-contracts',
      component: () => import('@/pages/evm/EvmAccountsPage.vue'),
      props: {mode: 'contract'},
    },
    {
      path: '/evm/tokens',
      name: 'evm-tokens',
      component: () => import('@/pages/evm/EvmAccountsPage.vue'),
      props: {mode: 'token'},
    },
    {
      path: '/evm/addresses',
      name: 'evm-addresses',
      component: () => import('@/pages/evm/EvmAccountsPage.vue'),
      props: {mode: undefined},
    },
    {
      path: '/evm/account/:addr',
      name: 'evm-account',
      component: () => import('@/pages/evm/EvmAccountPage.vue'),
      props: true,
    },
    {
      path: '/evm/token/:addr',
      name: 'evm-token',
      component: () => import('@/pages/evm/EvmTokenPage.vue'),
      props: true,
    },
    {
      path: '/evm/tx/:id',
      name: 'evm-tx',
      component: () => import('@/pages/evm/EvmTxPage.vue'),
      props: true,
    },
    // The section is a route param rather than a fragment: this router is on
    // hash history, so a second '#' in the URL is not something a browser will
    // hand back intact. /#/docs/limits is the shareable form.
    {
      path: '/docs/:section?',
      name: 'docs',
      component: () => import('@/pages/DocsPage.vue'),
      props: true,
    },
    {path: '/:pathMatch(.*)*', redirect: '/'},
  ],
  // Landing on a docs section scrolls to it rather than to the top; the page
  // does that itself once the section has rendered.
  scrollBehavior: (to) => (to.name === 'docs' && to.params.section ? false : {top: 0}),
})
