import {createApp} from 'vue'
import {useTheme} from 'nom-ui'
import AdminApp from './AdminApp.vue'
import '../style.css'

// A separate entry point, not a route on the public router (see
// admin/index.html and vite.config.ts): the admin bundle must never ship
// inside the viewer's, and the viewer must never import anything from here.

const {initTheme, setTheme} = useTheme()
if (localStorage.getItem('nom-wallet-theme') === null) {
  setTheme('dark')
} else {
  void initTheme()
}

createApp(AdminApp).mount('#app')
