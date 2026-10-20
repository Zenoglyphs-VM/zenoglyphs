import {createApp} from 'vue'
import {useTheme} from 'nom-ui'
import App from './App.vue'
import {router} from './router'
import './style.css'

// Dark is the default, not "follow the OS".
//
// The viewer is mostly a frame around other people's content, and inscribed
// images and SVGs are authored against the dark chrome the old viewer shipped.
// Following the OS would flip that for anyone on a light desktop, so the
// preference starts at dark and only the header toggle changes it — after
// which useTheme's stored preference wins on every later visit.
const {initTheme, setTheme} = useTheme()
if (localStorage.getItem('nom-wallet-theme') === null) {
  setTheme('dark')
} else {
  void initTheme()
}

createApp(App).use(router).mount('#app')
