'use strict';
(() => {
  const viewer = document.querySelector('#image-viewer');
  if (!viewer || typeof viewer.showModal !== 'function') return;
  const stage = document.querySelector('#image-stage');
  const status = document.querySelector('#image-status');
  let currentImage;
  document.querySelectorAll('.file-thumbnail img').forEach(img => {
    const label = img.parentElement.querySelector('span');
    const loaded = () => { label.hidden = true; };
    const failed = () => { img.hidden = true; label.hidden = false; };
    img.addEventListener('load', loaded);
    img.addEventListener('error', failed);
    if (img.complete) { if (img.naturalWidth) loaded(); else failed(); }
  });
  document.querySelectorAll('.image-preview-link').forEach(link => {
    link.addEventListener('click', event => {
      if (event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
      event.preventDefault();
      document.querySelector('#image-title').textContent = link.dataset.name;
      document.querySelector('#image-download').href = link.dataset.download;
      status.hidden = false;
      status.classList.remove('failed');
      status.textContent = 'Loading image…';
      const img = new Image();
      currentImage = img;
      img.alt = link.dataset.name;
      img.hidden = true;
      img.addEventListener('load', () => {
        if (currentImage !== img) return;
        img.hidden = false;
        status.hidden = true;
      });
      img.addEventListener('error', () => {
        if (currentImage !== img) return;
        status.textContent = 'Cannot display this image. It may have expired or be in an unsupported format.';
        status.classList.add('failed');
      });
      img.addEventListener('click', () => img.classList.toggle('actual-size'));
      stage.replaceChildren(img);
      stage.scrollTo(0, 0);
      viewer.showModal();
      img.src = link.href;
    });
  });
  document.querySelector('#image-close').addEventListener('click', () => viewer.close());
  viewer.addEventListener('click', event => {
    const rect = viewer.getBoundingClientRect();
    if (event.target === viewer && (event.clientX < rect.left || event.clientX > rect.right ||
        event.clientY < rect.top || event.clientY > rect.bottom)) viewer.close();
  });
  viewer.addEventListener('close', () => {
    currentImage = null;
    stage.replaceChildren();
  });
})();
