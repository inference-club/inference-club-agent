import requests
import logging
import time
from requests.exceptions import ConnectionError, RequestException

logger = logging.getLogger(__name__)

class ComfyAPI:
    def __init__(self, server_address):
        self.server_address = server_address.rstrip('/')
        self.client_id = str(int(time.time() * 1000))
        self.max_retries = 3
        self.retry_delay = 5  # seconds
        logger.info(f"Initialized ComfyAPI with server address: {server_address}")
        logger.info(f"Generated client ID: {self.client_id}")

    def _check_server_connection(self):
        """Check if the ComfyUI server is accessible"""
        try:
            response = requests.get(f"{self.server_address}/system_stats")
            return response.status_code == 200
        except ConnectionError:
            return False

    def queue_prompt(self, workflow):
        """Queue a prompt to the ComfyUI server with retry logic"""
        for attempt in range(self.max_retries):
            try:
                if not self._check_server_connection():
                    raise ConnectionError("ComfyUI server is not accessible")

                p = requests.post(f"{self.server_address}/prompt", json={
                    "prompt": workflow,
                    "client_id": self.client_id
                })
                response = p.json()

                if p.status_code != 200:
                    logger.error(f"Server returned status code {p.status_code}")
                    logger.error(f"Response: {response}")
                    if attempt < self.max_retries - 1:
                        time.sleep(self.retry_delay)
                        continue
                    return None
                return response
            except (ConnectionError, RequestException) as e:
                logger.error(f"Failed to queue prompt (attempt {attempt + 1}/{self.max_retries}): {str(e)}")
                if attempt < self.max_retries - 1:
                    time.sleep(self.retry_delay)
                    continue
                return None
        return None

    def get_image(self, filename, subfolder, folder_type):
        """Get an image from the ComfyUI server"""
        logger.info(f"Attempting to get image: {filename}")
        try:
            response = requests.get(f"{self.server_address}/view?filename={filename}&subfolder={subfolder}&type={folder_type}")
            if response.status_code != 200:
                logger.error(f"Failed to get image. Status code: {response.status_code}")
                return None
            return response
        except requests.exceptions.RequestException as e:
            logger.error(f"Failed to get image: {str(e)}")
            return None

    def get_history(self):
        """Get the history of prompts"""
        logger.info("Fetching prompt history...")
        try:
            response = requests.get(f"{self.server_address}/history")
            if response.status_code != 200:
                logger.error(f"Failed to get history. Status code: {response.status_code}")
                return None
            return response.json()
        except requests.exceptions.RequestException as e:
            logger.error(f"Failed to get history: {str(e)}")
            return None

    def wait_for_prompt_completion(self, prompt_id, timeout=300, check_interval=1):
        """Wait for a prompt to complete and return the result"""
        start_time = time.time()
        while time.time() - start_time < timeout:
            history = self.get_history()
            if history and prompt_id in history:
                prompt_data = history[prompt_id]
                if prompt_data.get('outputs'):
                    return prompt_data
                if prompt_data.get('error'):
                    raise Exception(f"Prompt failed: {prompt_data['error']}")
            time.sleep(check_interval)
        raise TimeoutError(f"Prompt {prompt_id} did not complete within {timeout} seconds")